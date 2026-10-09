package kvlite

import("context";"fmt";"testing";"time")
func TestLiveSemanticNegativeTemp(t *testing.T){
 bases:=[]string{"公司打印机无法打印应该怎么处理","如何重置登录密码","如何修改绑定手机号","如何申请订单退款","电脑无法连接无线网络","服务器启动失败如何排查","数据库连接超时怎么办","如何查看系统日志","接口返回500错误","文件上传失败怎么处理","如何申请发票","账号被锁定怎么办","如何清理磁盘空间","消息队列堆积如何处理","如何配置定时任务","服务端口被占用怎么办","如何导出用户数据","缓存数据不一致如何排查","如何升级应用版本","请求接口返回401"}
 pos:=[]string{"办公打印机不能使用怎么办","忘记密码后怎样重新设置登录密码","账号安全页面怎么更换手机号码","买的东西不想要了怎样办理退款","笔记本连不上WiFi怎么解决","服务程序起不来应该检查哪些地方","应用访问数据库一直超时如何处理","在哪里可以找到服务器运行日志","API调用出现内部服务器错误怎么办","上传附件一直不成功如何解决","订单完成后怎样开具发票","用户登录次数过多被锁了如何解锁","服务器磁盘快满了怎样释放空间","队列里的任务越来越多怎么排查","怎样设置每天自动执行的任务","启动程序提示端口已经使用如何解决","怎样把系统里的用户信息导出来","缓存和数据库内容不同怎么办","线上服务怎样进行版本更新","调用API提示没有权限怎么处理"}
 neg:=[]string{"今天北京天气怎么样","推荐一部好看的电影","怎么做红烧肉","附近有哪些旅游景点","孩子应该如何学习英语","股票市场今天走势如何","怎样练习瑜伽减肥","世界上最高的山是哪座","如何种植阳台蔬菜","给我讲一个睡前故事","新能源汽车哪个品牌好","如何拍出好看的照片","最近有什么热门音乐","猫咪为什么喜欢睡觉","怎样提高跑步速度","推荐一本科幻小说","如何学习绘画","周末去哪里露营","咖啡怎么冲泡","为什么天空是蓝色的"}
 store,err:=NewStore(DefaultConfig("127.0.0.1:28080"));if err!=nil{t.Fatal(err)};defer store.Close();ctx,cancel:=context.WithTimeout(context.Background(),90*time.Second);defer cancel();scope:=LogicalScope{Tenant:"semantic-neg-temp",KnowledgeBase:"ai-chat",CorpusVersion:"v1"};docs:=make([]Document,0,100)
 defer func(){for _,d:=range docs{_ = store.Delete(context.Background(),d.Question,scope)}}()
 for i:=0;i<100;i++{d:=Document{Question:fmt.Sprintf("%s（测试场景%d）",bases[i%20],i),Scope:scope,Answer:AnswerEnvelope{Version:1,DocumentID:fmt.Sprintf("neg-%03d",i),Answer:"这是语义负样本测试答案。",Source:"knowledge_base",CreatedAt:time.Now().Unix()}};docs=append(docs,d);if err:=store.Upsert(ctx,d);err!=nil{t.Fatal(err)}}
 pmin:=2.0;pmax:=-1.0;psum:=0.0; nmax:=-1.0; nsum:=0.0
 search:=func(q string)[]Hit{for deadline:=time.Now().Add(10*time.Second);time.Now().Before(deadline);time.Sleep(100*time.Millisecond){h,e:=store.Search(ctx,q,scope,1,-1);if e==nil&&len(h)>0{return h}};return nil}
 for i,q:=range pos{h:=search(q);if len(h)==0{t.Fatalf("positive query %d no hit",i)};if h[0].Score<pmin{pmin=h[0].Score};if h[0].Score>pmax{pmax=h[0].Score};psum+=h[0].Score}
 for i,q:=range neg{h:=search(q);if len(h)==0{t.Fatalf("negative query %d no hit",i)};if h[0].Score>nmax{nmax=h[0].Score};nsum+=h[0].Score}
 t.Logf("positive min=%.4f avg=%.4f max=%.4f negative avg=%.4f max=%.4f",pmin,psum/20,pmax,nsum/20,nmax)
}
