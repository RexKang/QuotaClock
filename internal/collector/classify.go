// Package collector 承载后台采集：出站客户端与错误分类、退避状态机、快照 store
// （revision 五类触发 diff）、调度循环（tick 锚定 + 平台错峰并发）。
package collector

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrClass 错误分类（设计 §7.2 表驱动）。
type ErrClass string

const (
	ClassOK           ErrClass = "OK"
	ClassNetwork      ErrClass = "NETWORK"       // 连接失败/DNS/读超时 → 指数退避
	ClassServerErr    ErrClass = "SERVER_ERR"    // 5xx → 指数退避
	ClassTokenInvalid ErrClass = "TOKEN_INVALID" // 401/403 → 停采该平台，不退避
	ClassRateLimited  ErrClass = "RATE_LIMITED"  // 429 → 按 Retry-After 顺延
	ClassClientErr    ErrClass = "CLIENT_ERR"    // 其他 4xx → WARN + 退避
	ClassBizError     ErrClass = "BIZ_ERROR"     // 200 但 success≠true / 有 error 键 → 退避
)

// Classified 单次 fetch 的分类结果。
type Classified struct {
	Class       ErrClass
	Status      int
	Data        json.RawMessage // Class==OK 时有效（JSON 原文或带引号字符串）
	Message     string          // 快照稳定文案（不随轮询变化；error.message 纪律，PRD 附录 A）
	Summary     string          // 提取的上游错误摘要（msg / error.message；/api/test 502 透传用）
	RetryAfterS *int            // RATE_LIMITED 时来自 Retry-After 头
}

// 快照稳定文案（失败态在页面的两种文案严格区分：token 失效 ≠ 网络失败，PRD §8）。
const (
	msgTokenInvalid = "token 失效，请在设置中更新"
	msgNetwork      = "网络错误或请求超时"
)

// ClassifyTransport 传输层失败（含 25s 超时）一律 NETWORK。
func ClassifyTransport(err error) Classified {
	return Classified{Class: ClassNetwork, Message: msgNetwork}
}

// ClassifyHTTP 依状态码与 body 分类；HTTP 200 且 body 为 JSON 对象时做业务级 ok 判定（§7.5）。
// 非 JSON 响应按原文透传、以 HTTP 状态为准。
func ClassifyHTTP(status int, header http.Header, body []byte) Classified {
	cl := Classified{Status: status}
	switch {
	case status == 401 || status == 403:
		cl.Class = ClassTokenInvalid
		cl.Message = msgTokenInvalid
		cl.Summary = bizSummaryFromBody(body)
		return cl
	case status == 429:
		cl.Class = ClassRateLimited
		cl.Message = "触发上游限流（HTTP 429）"
		cl.Summary = bizSummaryFromBody(body)
		if r := ParseRetryAfter(header.Get("Retry-After"), time.Now()); r != nil {
			cl.RetryAfterS = r
		}
		return cl
	case status >= 500:
		cl.Class = ClassServerErr
		cl.Message = fmt.Sprintf("上游服务错误（HTTP %d）", status)
		cl.Summary = bizSummaryFromBody(body)
		return cl
	case status >= 400:
		cl.Class = ClassClientErr
		cl.Message = fmt.Sprintf("请求被拒绝（HTTP %d）", status)
		cl.Summary = bizSummaryFromBody(body)
		return cl
	case status >= 200 && status < 300:
		if obj := jsonObject(body); obj != nil {
			// 业务级 ok 判定（复刻 v0.1）：success 键存在且 ≠ true → 失败；error 键存在 → 失败
			if v, ok := obj["success"]; ok && !jsonTrue(v) {
				cl.Class = ClassBizError
				cl.Summary = bizMessage(obj, status)
				cl.Message = cl.Summary
				return cl
			}
			if _, ok := obj["error"]; ok {
				cl.Class = ClassBizError
				cl.Summary = bizMessage(obj, status)
				cl.Message = cl.Summary
				return cl
			}
		}
		cl.Class = ClassOK
		cl.Data = dataOf(body)
		return cl
	default:
		cl.Class = ClassClientErr
		cl.Message = fmt.Sprintf("异常响应（HTTP %d）", status)
		return cl
	}
}

// jsonTrue 判断 success 键是否严格等于 true（复刻 v0.1 的 data.success === true）。
func jsonTrue(raw json.RawMessage) bool {
	var b bool
	return json.Unmarshal(raw, &b) == nil && b
}

// bizSummaryFromBody 从错误响应 body 提取摘要（msg → error.message），非 JSON 对象返回空。
// 仅用于 /api/test 的 502 透传纪律；快照 error.message 用分类级稳定文案（message 纪律）。
func bizSummaryFromBody(body []byte) string {
	if obj := jsonObject(body); obj != nil {
		if v, ok := obj["msg"]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				return s
			}
		}
		if v, ok := obj["error"]; ok {
			var e struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(v, &e) == nil && e.Message != "" {
				return e.Message
			}
		}
	}
	return ""
}

// bizMessage 失败文案提取顺序：msg → error.message → "HTTP <status>"（§7.5）。
func bizMessage(obj map[string]json.RawMessage, status int) string {
	if v, ok := obj["msg"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			return s
		}
	}
	if v, ok := obj["error"]; ok {
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(v, &e) == nil && e.Message != "" {
			return e.Message
		}
	}
	return fmt.Sprintf("HTTP %d", status)
}

// jsonObject body 为 JSON 对象时返回其键集，否则 nil。
func jsonObject(body []byte) map[string]json.RawMessage {
	trimmed := strings.TrimSpace(string(body))
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return nil
	}
	return m
}

// dataOf 合法 JSON → 原文透传；否则（如 opencode SolidStart 序列化文本）→ 带引号字符串透传。
func dataOf(body []byte) json.RawMessage {
	if json.Valid(body) {
		return json.RawMessage(body)
	}
	b, err := json.Marshal(string(body))
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}

// ParseRetryAfter 解析 Retry-After：整数秒或 HTTP 日期；无法解析返回 nil。
func ParseRetryAfter(v string, now time.Time) *int {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			secs = 0
		}
		return &secs
	}
	if t, err := http.ParseTime(v); err == nil {
		d := int(t.Sub(now).Seconds() + 0.5)
		if d < 0 {
			d = 0
		}
		return &d
	}
	return nil
}
