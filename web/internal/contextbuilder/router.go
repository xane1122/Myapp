package contextbuilder

import "strings"

func RouteMemory(message string, index []MemoryIndexItem) MemoryRouteDecision {
	text := normalized(message)
	if text == "" {
		return MemoryRouteDecision{Reason: "当前问题为空"}
	}
	if matchesMemoryListIntent(text) {
		return MemoryRouteDecision{
			NeedSearchMemory: true,
			Query:            "长期记忆列表 全部记忆",
			Reason:           "用户询问长期记忆清单",
		}
	}
	if matchesProfileIntent(text) {
		return MemoryRouteDecision{
			NeedSearchMemory: true,
			Query:            strings.TrimSpace(message),
			Reason:           "用户要求基于全局记忆介绍其画像、喜好、习惯或边界",
		}
	}
	if query, ok := personalFactQuery(text); ok {
		return MemoryRouteDecision{
			NeedSearchMemory:  true,
			Query:             query,
			RequireQueryMatch: true,
			Reason:            "用户询问需要从长期记忆确认的个人事实",
		}
	}
	if matchesRecallIntent(text) {
		ids := matchMemoryIDs(text, index)
		return MemoryRouteDecision{
			NeedSearchMemory: true,
			TargetMemoryIDs:  ids,
			Query:            strings.TrimSpace(message),
			Reason:           "用户引用旧对话或旧记忆",
		}
	}
	ids := matchMemoryIDs(text, index)
	if len(ids) > 0 {
		return MemoryRouteDecision{
			NeedSearchMemory: true,
			TargetMemoryIDs:  ids,
			Query:            strings.TrimSpace(message),
			Reason:           "用户当前话题命中长期记忆索引关键词",
		}
	}
	return MemoryRouteDecision{Reason: "当前问题不需要旧记忆"}
}

func matchesProfileIntent(text string) bool {
	return containsAny(text, []string{
		"介绍我", "描述我", "评价我", "你眼中的我", "你觉得我是", "关于我", "了解我多少",
		"我喜欢什么", "我的喜好", "我的偏好", "我的习惯", "我的雷区", "我的边界", "我的禁忌",
		"introduce me", "describe me", "what do i like", "my preferences", "what do you know about me",
	})
}

func personalFactQuery(text string) (string, bool) {
	if containsAny(text, []string{
		"我有什么病", "我得了什么病", "我有啥病", "我的病", "我的疾病", "我的诊断", "我的病史",
		"我身体怎么样", "我的健康", "我的心理问题", "我的精神疾病",
	}) {
		return "健康状况 疾病 病史 诊断 双相 情感障碍 心理问题 自残 治疗 用药", true
	}
	if containsAny(text, []string{
		"我们什么关系", "我们是什么关系", "你和我什么关系", "你和我是什么关系", "我们的关系",
	}) {
		return "关系 恋爱 伴侣 同学 朋友 相处设定", true
	}
	if containsAny(text, []string{
		"我叫什么", "我的名字", "我多大", "我的年龄", "我在哪上学", "我在哪里上学", "我的学校",
	}) {
		return "身份 名字 年龄 生日 学校 年级", true
	}
	return "", false
}

func RouteDoc(message string, docs []UploadedDocumentIndexItem) DocRouteDecision {
	text := normalized(message)
	if text == "" {
		return DocRouteDecision{Reason: "当前问题为空"}
	}
	hasDoc := containsAny(text, []string{"文档", "文件", "pdf", "doc", "file", "karpathy", "claude"})
	if !hasDoc {
		return DocRouteDecision{Reason: "当前问题不需要上传文档正文"}
	}
	needsBody := containsAny(text, []string{"写了什么", "说了什么", "正文", "内容", "读取", "读一下", "打开", "看一下", "根据", "按照", "分析", "总结", "play game", "游戏", "扮演"})
	if !needsBody {
		return DocRouteDecision{Reason: "当前问题只涉及文档目录"}
	}
	var ids []int64
	for _, doc := range docs {
		if strings.Contains(text, strings.ToLower(doc.Title)) {
			ids = append(ids, doc.DocID)
		}
	}
	return DocRouteDecision{
		NeedReadUploadedDoc: true,
		TargetDocIDs:        ids,
		Query:               strings.TrimSpace(message),
		Reason:              "用户询问上传文档正文或要求基于文档处理",
	}
}

func RouteTime(message string) TimeRouteDecision {
	text := normalized(message)
	if containsAny(text, []string{"现在几点", "几点了", "当前时间", "今天几号", "星期几", "周几", "current time", "current date"}) {
		return TimeRouteDecision{NeedCurrentTime: true, Reason: "用户询问实时日期时间"}
	}
	return TimeRouteDecision{Reason: "当前问题不需要实时时间"}
}

func RouteServer(message string) ServerRouteDecision {
	text := normalized(message)
	if containsAny(text, []string{
		"linux shell", "shell", "执行命令", "运行命令", "服务器文件", "部署目录", "读取文件", "查看文件", "/opt/myapp",
		"删除上传文档", "删除文档", "删文档", "删除文件", "删文件", "删除长期记忆", "删记忆", "删除表情包", "删除人设", "清理上传",
		"ls ", "cat ", "pwd", "find ", "grep ", "rg ", "sed ", "rm ", "sqlite3 ",
	}) {
		return ServerRouteDecision{NeedLinuxShell: true, Reason: "用户要求读取服务器状态或运行命令"}
	}
	return ServerRouteDecision{Reason: "当前问题不需要服务器状态"}
}

func matchesMemoryListIntent(text string) bool {
	if !(strings.Contains(text, "记忆") || strings.Contains(text, "memory")) {
		return false
	}
	return containsAny(text, []string{"有哪些", "哪些", "清单", "列表", "列出", "全部", "所有", "当前", "保存了什么", "写入", "list", "show", "all", "memories"})
}

func matchesRecallIntent(text string) bool {
	return containsAny(text, []string{
		"还记得", "记不记得", "你记得", "以前", "之前", "上次", "昨天", "刚才", "刚刚", "前面说", "前面聊",
		"我说过", "那件事", "这件事", "那个事", "那个事情", "上次那个", "昨天那个", "之前那个", "刚才那个",
		"聊天记录", "历史记录", "上传文档", "历史文档", "那份文档", "那份文件", "那份 pdf", "pdf 里", "pdf里",
		"remember", "previously", "last time", "that thing",
	})
}

func matchMemoryIDs(text string, index []MemoryIndexItem) []int64 {
	var ids []int64
	add := func(id int64) {
		for _, existing := range ids {
			if existing == id {
				return
			}
		}
		ids = append(ids, id)
	}
	for _, item := range index {
		if strings.Contains(text, "memory_id:"+itoa64(item.MemoryID)) || strings.Contains(text, "memory "+itoa64(item.MemoryID)) {
			add(item.MemoryID)
			continue
		}
		for _, keyword := range item.Keywords {
			kw := strings.ToLower(strings.TrimSpace(keyword))
			if kw != "" && strings.Contains(text, kw) {
				add(item.MemoryID)
				break
			}
		}
		if item.Title != "" && strings.Contains(text, strings.ToLower(item.Title)) {
			add(item.MemoryID)
		}
	}
	return ids
}

func normalized(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
