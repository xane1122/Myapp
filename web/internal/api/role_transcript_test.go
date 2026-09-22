package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRoleTranscriptRecoversOnlyTerminalPayload(t *testing.T) {
	final := `{"thought":"最终角色心情","action":"笑了","reply":"已经查到了。","messages":["已经查到了。","这是最后的回复。"]}`
	transcript := "我需要先查询。\n\n" + `{"thought":"早期小心思","action":"准备查询","reply":"等我查","messages":["等我查"]}` + "\n\n[Calling tool get_current_time]\n[Calling tool manage_life_data with inputs: {\"operation\":\"query\"}]\nBased on the tool results:\n- 当前结果\n\n" + final
	recovered, ok := recoverFinalRoleReplyJSON(transcript)
	if !ok || recovered != final {
		t.Fatal("terminal role payload was not recovered")
	}
	parsed, ok := parseRoleReply(transcript)
	if !ok || parsed.Reply != "已经查到了。" || parsed.Thought != "最终角色心情" || parsed.Action != "笑了" || len(parsed.Messages) != 2 {
		t.Fatal("draft leaked into final role fields")
	}
	if filtered := stripInternalReasoning(transcript); filtered != final {
		t.Fatal("stream/nonstream provider content includes transcript")
	}
}

func TestRoleTranscriptPreservesValidRepliesAndOrdinaryCode(t *testing.T) {
	for _, text := range []string{
		"正常中文回复，不是草稿。",
		`{"thought":"角色心情","action":"微笑","reply":"正文包含花括号 { } 和转义引号 \"保持\"","messages":[]}`,
		"示例代码：\n```json\n" + `{"thought":"示例","action":"示例","reply":"示例"}` + "\n```\n这只是代码说明。",
		"说明 [Calling tool get_current_time] 是一个字符串。",
		"```text\n[Calling tool get_current_time]\nBased on the tool results:\n```",
	} {
		if _, ok := recoverFinalRoleReplyJSON(text); ok {
			t.Fatal("ordinary content mistaken for transcript")
		}
		if got := stripInternalReasoning(text); got != strings.TrimSpace(text) {
			t.Fatal("ordinary reply changed")
		}
	}
}

func TestRoleTranscriptRejectsIncompleteFinalDraft(t *testing.T) {
	for _, text := range []string{
		`[Calling tool get_current_time]`,
		"Based on the tool results:\n结果说明而非最终回复",
		`{"thought":"未完成角色内心","action":"`,
		"[Calling tool get_current_time]\n" + `{"thought":"还在查","action":"","reply":"","messages":[]}`,
		`{"thought":"第一稿","action":"","reply":"第一稿"}` + "\n" + `{"thought":"终稿","action":"","reply":"终稿"}` + "\n还有未经协议包裹的草稿",
	} {
		if _, ok := recoverFinalRoleReplyJSON(text); ok {
			t.Fatal("unfinished transcript accepted")
		}
		if !suspiciousAssistantDraft(text) {
			t.Fatal("unfinished transcript not blocked")
		}
		if stripInternalReasoning(text) != "" {
			t.Fatal("unfinished transcript may leak through streaming")
		}
	}
}

func TestRoleTranscriptKeepsFinalMessageListWithoutReply(t *testing.T) {
	text := "[Calling tool get_current_time]\n" + `{"thought":"当前心情","action":"","reply":"","messages":["最终正文"]}`
	if _, ok := recoverFinalRoleReplyJSON(text); !ok {
		t.Fatal("valid final message list not recovered")
	}
	parsed, ok := parseRoleReply(text)
	if !ok || len(parsed.Messages) != 1 || parsed.Messages[0] != "最终正文" {
		t.Fatal("final messages changed")
	}
}

func TestRoleTranscriptRecoversMalformedTerminalPayload(t *testing.T) {
	malformed := `{"thought":"她说"别走"的时候，我心里一紧。","action":"把她抱稳。","reply":"我没走，"宝宝"，我在这儿。","messages":["我没走。","我在这儿。"],"quote_message_id":7}`
	recovered, ok := recoverMalformedRoleReplyJSON(malformed)
	if !ok {
		t.Fatal("malformed terminal role payload was not recovered")
	}
	var parsed roleReply
	if err := json.Unmarshal([]byte(recovered), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Thought != `她说"别走"的时候，我心里一紧。` || parsed.Action != "把她抱稳。" || parsed.Reply != `我没走，"宝宝"，我在这儿。` {
		t.Fatalf("unexpected recovered payload: %#v", parsed)
	}
	if len(parsed.Messages) != 0 || parsed.QuoteMessageID != 0 {
		t.Fatalf("untrusted trailing fields were retained: %#v", parsed)
	}
	if filtered := stripInternalReasoning(malformed); filtered != recovered {
		t.Fatalf("provider filter did not keep recovered payload: %q", filtered)
	}
}

func TestRoleTranscriptRecoversOnlyMalformedTerminalObject(t *testing.T) {
	prior := `{"thought":"先前想法","action":"等待","reply":"等我一下。","messages":["等我一下。"]}`
	final := `{"thought":"她在等我。","action":"走回来。","reply":"查完了，"宝贝"。","messages":["查完了。"]}`
	transcript := prior + "\n[Calling tool get_current_time]\nBased on the tool results:\n- result\n" + final
	recovered := stripInternalReasoning(transcript)
	parsed, ok := parseRoleReply(recovered)
	if !ok || parsed.Reply != `查完了，"宝贝"。` {
		t.Fatalf("terminal malformed reply was not isolated: %#v, %q", parsed, recovered)
	}

	unsafe := final + "\n还有未经协议包裹的分析"
	if _, ok := recoverMalformedRoleReplyJSON(unsafe); ok || stripInternalReasoning(unsafe) != "" {
		t.Fatal("trailing analysis must remain blocked")
	}
}
