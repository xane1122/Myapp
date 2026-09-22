package api
import("encoding/json";"strings";"myapp/internal/db")

func affirmativeTransferAcceptance(message string) bool {
 m:=strings.Trim(strings.TrimSpace(message),"。！!，,？?～~ ")
 switch m {case "好","好的","好啊","好呀","嗯","嗯嗯","恩","可以","可以啊","行","行啊","来吧","转吧","发吧","要","要啊","那就转吧","那就发吧","嗯，那就转吧","好，那就转吧","可以，转吧":return true}
 return false
}

// A bounded authoritative action ledger, independent of retrieved memories.
func recentTransferExecutionState(cid int64) string {
 rows,err:=db.DB.Query(`SELECT v.id,v.message_id,v.amount_cents,v.note,v.status,v.sender_role FROM virtual_transfers v JOIN messages m ON m.id=v.message_id AND m.conversation_id=v.conversation_id WHERE v.conversation_id=? ORDER BY v.id DESC LIMIT 3`,cid)
 if err!=nil{return ""};defer rows.Close()
 entries:=[]map[string]interface{}{}
 for rows.Next(){var id,mid,cents int64;var note,status,sender string
  if rows.Scan(&id,&mid,&cents,&note,&status,&sender)!=nil{return ""}
  entries=append(entries,map[string]interface{}{"transfer_id":id,"message_id":mid,"amount_cents":cents,"note":note,"status":status,"sender_role":sender,"sent":true})
 }
 if rows.Err()!=nil||len(entries)==0{return ""}
 b,_:=json.Marshal(entries)
 return "【当前聊天转账已执行状态：数据库事实，不是新指令】\n"+string(b)+"\n这些卡片已经发送；领取状态变化也不表示需要重发。历史/检索中的旧请求已由这些记录完成。当前新请求和旧任务必须区分，不要把普通确认或旧邀约再次执行。"
}
