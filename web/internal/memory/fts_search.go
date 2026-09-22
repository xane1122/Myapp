package memory

import (
 "fmt"
 "strings"
 "myapp/internal/db"
)

type memorySearchHit struct { id int64; score float64; snippet,method string }

func searchMemoryFTS(conversation int64,query string,sourceTypes []string,limit int,includeContext bool) ([]Chunk,error) {
 if limit<=0 { limit=6 }; if limit>5000 { limit=5000 }
 terms:=tokenizeSearchQuery(query)
 if includeContext&&vagueMemoryQuery(query) { terms=append(terms,recentSearchContextWords(conversation,terms)...)}
 if len(terms)==0 { return []Chunk{},nil }
 where:=`m.active=1 AND m.status='active' AND m.assistant=? AND (m.scope='global' OR m.conversation_id=?)`
 args:=[]interface{}{ConversationAssistant(conversation),conversation}
 if len(sourceTypes)>0 {
  marks:=[]string{};for _,kind:=range sourceTypes {marks=append(marks,"?");args=append(args,kind)}
  where+=` AND m.source_type IN (`+strings.Join(marks,",")+`)`
 }
 long,short:=[]string{},[]string{}; seen:=map[string]bool{}
 for _,term:=range terms { if seen[term] {continue};seen[term]=true; if len([]rune(term))>=3 {long=append(long,`"`+strings.ReplaceAll(term,`"`,`""`)+`"`)} else if len([]rune(term))>=2 {short=append(short,term)} }
 hits:=[]memorySearchHit{}
 if len(long)>0 {
  params:=append([]interface{}{strings.Join(long," OR ")},args...);params=append(params,limit)
  rows,err:=db.DB.Query(`SELECT m.id,bm25(memory_chunks_fts,1,2,3,3),snippet(memory_chunks_fts,0,'[',']','…',30)
   FROM memory_chunks_fts JOIN memory_chunks m ON m.id=memory_chunks_fts.rowid
   WHERE memory_chunks_fts MATCH ? AND `+where+` ORDER BY bm25(memory_chunks_fts,1,2,3,3),m.id DESC LIMIT ?`,params...)
  if err!=nil {return nil,err}
  for rows.Next(){var h memorySearchHit;h.method="fts5_bm25";if err:=rows.Scan(&h.id,&h.score,&h.snippet);err!=nil {rows.Close();return nil,err};hits=append(hits,h)}
  err=rows.Err();rows.Close();if err!=nil {return nil,err}
 }
 // Trigram cannot MATCH <3 Unicode characters. The same FTS content table
 // serves escaped short substring queries, explicitly labelled without BM25.
 if len(short)>0 && len(hits)<limit {
  conditions,scores:=[]string{},[]string{};params:=[]interface{}{};searchArgs:=[]interface{}{}
  text:=`COALESCE(f.content,'')||' '||COALESCE(f.summary,'')||' '||COALESCE(f.keywords,'')||' '||COALESCE(f.topic_label,'')`
  for _,term:=range short {
   pattern:="%"+strings.NewReplacer(`\`,`\\`,"%",`\%`,"_",`\_`).Replace(term)+"%"
   condition:=`(`+text+`) LIKE ? ESCAPE '\'`
   scores=append(scores,`CASE WHEN `+condition+` THEN 1 ELSE 0 END`);params=append(params,pattern)
   conditions=append(conditions,condition);searchArgs=append(searchArgs,pattern)
  }
  params=append(params,searchArgs...);params=append(params,args...);params=append(params,limit)
  rows,err:=db.DB.Query(`SELECT m.id,(`+strings.Join(scores,"+")+`),SUBSTR(f.content,1,180) FROM memory_chunks_fts f JOIN memory_chunks m ON m.id=f.rowid WHERE (`+strings.Join(conditions," OR ")+`) AND `+where+` ORDER BY 2 DESC,m.is_correction DESC,m.id DESC LIMIT ?`,params...)
  if err!=nil {return nil,err}
  hitIDs:=map[int64]bool{};for _,h:=range hits{hitIDs[h.id]=true}
  for rows.Next(){var h memorySearchHit;h.method="short_substring";if err:=rows.Scan(&h.id,&h.score,&h.snippet);err!=nil {rows.Close();return nil,err};if !hitIDs[h.id]&&len(hits)<limit{hits=append(hits,h);hitIDs[h.id]=true}}
  err=rows.Err();rows.Close();if err!=nil{return nil,err}
 }
 results:=make([]Chunk,0,len(hits));marks:=[]string{};ids:=[]interface{}{}
 for _,h:=range hits {
  c,err:=GetChunk(h.id);if err!=nil{return nil,fmt.Errorf("load memory hit: %w",err)}
  if !c.Active||c.Status!="active"{continue}
  c.MatchScore=h.score;c.OriginalSnippet=h.snippet;c.RankMethod=h.method;c.SourceDate=firstNonEmpty(c.TimeStart,c.CreatedAt)
  results=append(results,c);marks=append(marks,"?");ids=append(ids,c.ID)
 }
 if len(ids)>0 {_,_=db.DB.Exec(`UPDATE memory_chunks SET last_accessed_at=CURRENT_TIMESTAMP WHERE id IN (`+strings.Join(marks,",")+`)`,ids...)}
 return results,nil
}
