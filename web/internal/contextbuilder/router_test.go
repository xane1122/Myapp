package contextbuilder

import (
	"strings"
	"testing"
)

func TestRouteMemory(t *testing.T) {
	index := []MemoryIndexItem{{MemoryID: 7, Summary: "四级考试时间", Keywords: []string{"四级", "考试"}, Sensitivity: SensitivityMedium}}
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{"recall", "你记得我之前说过什么吗", true},
		{"list", "当前长期记忆有哪些", true},
		{"profile", "介绍我，我平时喜欢什么？", true},
		{"preferences", "你知道我的偏好和雷区吗", true},
		{"health fact", "我有什么病", true},
		{"relationship fact", "我们是什么关系", true},
		{"identity fact", "我多大", true},
		{"keyword", "四级时间是多少", true},
		{"technical", "Go interface nil 是什么", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RouteMemory(tt.msg, index)
			if got.NeedSearchMemory != tt.want {
				t.Fatalf("NeedSearchMemory = %v, want %v; got %#v", got.NeedSearchMemory, tt.want, got)
			}
		})
	}
}

func TestRouteMemoryExpandsPersonalFactQueries(t *testing.T) {
	tests := []struct {
		message string
		terms   []string
	}{
		{"我有什么病", []string{"健康", "诊断", "双相", "自残"}},
		{"我们是什么关系", []string{"关系", "恋爱", "同学"}},
		{"我多大", []string{"身份", "年龄", "生日"}},
	}
	for _, test := range tests {
		route := RouteMemory(test.message, nil)
		if !route.NeedSearchMemory {
			t.Fatalf("personal fact did not trigger recall: %q", test.message)
		}
		if !route.RequireQueryMatch {
			t.Fatalf("personal fact recall must reject unrelated context matches: %q", test.message)
		}
		for _, term := range test.terms {
			if !strings.Contains(route.Query, term) {
				t.Fatalf("query %q for %q is missing %q", route.Query, test.message, term)
			}
		}
	}
}

func TestRouteDocTimeAndServer(t *testing.T) {
	docs := []UploadedDocumentIndexItem{{DocID: 1, Title: "Karpathy-CLAUDE", Summary: "AI guideline", Sensitivity: SensitivityLow}}
	if got := RouteDoc("那份 Karpathy-CLAUDE 文档里说了什么？", docs); !got.NeedReadUploadedDoc {
		t.Fatalf("expected doc route, got %#v", got)
	}
	if got := RouteTime("今天星期几？"); !got.NeedCurrentTime {
		t.Fatalf("expected time route, got %#v", got)
	}
	if got := RouteServer("看看 /opt/myapp 目录下有什么。"); !got.NeedLinuxShell {
		t.Fatalf("expected server route, got %#v", got)
	}
}
