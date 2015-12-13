package forum_page_monitor

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/purstal/go-tieba-base/tieba"
	"github.com/purstal/go-tieba-base/tieba/apis"
	"github.com/purstal/go-tieba-base/tieba/apis/forum-win8-1.5.0.0"
)

type FreshPostMonitor struct {
	forum_page_monitor *ForumPageMonitor
	actChan            chan action
	PageChan           chan ForumPage
}

func useless() {
	fmt.Println(json.Marshal(nil))
}

func (filter *FreshPostMonitor) ChangeInterval(newInterval time.Duration) {
	filter.forum_page_monitor.ChangeInterval(newInterval)
}

func (filter *FreshPostMonitor) Stop() {
	filter.actChan <- action{"Stop", nil}
}

func TryGettingUserName(acc *postbar.Account, uid uint64) string {
	if uid == 0 {
		return ""
	}
	for {
		info, err := apis.GetUserInfo(acc, uid)
		if err == nil {
			switch info.ErrorCode.(type) {
			case (float64):
				if info.ErrorCode.(float64) == 0 {
					return info.User.Name
				}
			case (string):
				if info.ErrorCode.(string) == "0" {
					return info.User.Name
				}
			}
		}
	}
}

type freshPostRecorder []dayRecord

type dayRecord struct {
	serverTime   time.Time
	foundPostMap map[uint64][]uint64 //tid->uids
}

func NewFreshPostMonitor(accWin8 *postbar.Account, kw string,
	interval time.Duration, fn func(ForumPage)) *FreshPostMonitor {

	/*
		2015-12-13 @purstal:
		莫名其妙出现一个问题, 删贴机监视自己个人贴吧时,
		每一定间隔会重复去搜索一个已搜索过的贴子, 而最后回复时间与最后回复者都未变化.
		由于高级搜索并没有立刻消去被删的回复, 导致回复重复被删, 暴露问题.
		待解决.
		TODO: 找出原因,然后解决这个问题.
	*/

	var monitor = FreshPostMonitor{}
	monitor.forum_page_monitor = NewForumPageMonitor(accWin8, kw, interval)
	monitor.actChan = make(chan action)
	monitor.PageChan = make(chan ForumPage)

	go func() {
		var serverTimeNowInt int64      //当前服务器时间.
		var logIDNow uint64             //似乎在一段时间内单增, 达到特定的条件归位.
		var lastFreshPostTime time.Time //上次最新的新回复的时间, 若上次无新贴, 则是上上次的, 以此类推.

		//目的记得是用来找到更多[[[同一秒内]同一个贴子]的多个回复].
		//key 为 tid, value 为最后回复人的 uid.
		var lastFreshPostMap = make(map[uint64]uint64)

	MainLoop:
		for {
			var forumPage ForumPage
			select {
			case forumPage = <-monitor.forum_page_monitor.PageChan:
				if fn != nil {
					fn(forumPage)
				}
			case act := <-monitor.actChan:
				switch act.action {
				case "stop":
					monitor.forum_page_monitor.Stop()
					close(monitor.actChan)
					break MainLoop //2015-12-14 @purstal:是不是当时忘加了?
				}
				continue
			}

			//获取主页是并发进行的, 以下用来防止由于获取到的主页时间顺序混乱导致处理混乱.
			//做法就是直接抛弃[更晚时间获得到的]可能应该是[更早时间的页面]
			//极端的做法是先稍稍延时获得到的页面的处理,但是没必要这么做.
			if forumPage.Extra.ServerTime.Unix() < serverTimeNowInt {
				fmt.Println("响应的服务器时间早于上次,忽略")
				continue
			} else if forumPage.Extra.ServerTime.Unix() == serverTimeNowInt && forumPage.Extra.LogID < logIDNow {
				fmt.Println("响应的服务器时间等于上次,且log_id小于上次,忽略")
				continue
			} else {
				logIDNow = forumPage.Extra.LogID
				serverTimeNowInt = forumPage.Extra.ServerTime.Unix()
			}

			//根据最后回复时间排序主页获得到的主题列表.
			//这样做的原因是,某次偶然发现某吧一个并非置顶的贴子却被固定在置顶之下,导致之前只忽略置顶的做法失去了效果.
			//另外需要注意的是,排序的算法需要是稳定的.
			var threadList = PageThreadsSorter(forumPage.ThreadList)
			sort.Sort(threadList)

			//当时貌似是如此区分本次和上次的:
			//last: 上次的内容; now: 本次的内容.
			var nowFreshPostTime time.Time
			//找到本次最新的新回复的时间.
			//其实因为已经sort过了, 所以直接取第一个的时间,再验证一下就好了吧...
			//等等, 若是贴吧抽风返回的时间都有问题, `nowFreshPostTime`不也就有问题了吗?
			//这次不提, 下次的`lastFreshPostTime`就会受到影响吧?
			//所以, 需要验证一下`nowFreshPostTime`是否等于0?
			//TODO: 等脑子清醒且有时间的时候考虑一下这件事...
			for _, thread := range threadList {
				if thread.LastReplyTime.Before(lastFreshPostTime) {
					if thread.IsTop {
						continue
					}
					break
				}
				if nowFreshPostTime.Before(thread.LastReplyTime) {
					nowFreshPostTime = thread.LastReplyTime
				}
			}

			//参见之前 lastFreshPostMap 注释
			var nowFreshPostMap map[uint64]uint64
			if nowFreshPostTime.Equal(lastFreshPostTime) {
				nowFreshPostMap = lastFreshPostMap
			} else {
				nowFreshPostMap = make(map[uint64]uint64)
			}

			var freshThreadList []*forum.ForumPageThread
			//这里开始, 过滤出新的贴子.
			for _, thread := range threadList {
				if thread.LastReplyTime.Before(lastFreshPostTime) {
					//依旧是旧的做法, 应该可以去掉的.
					if thread.IsTop {
						continue
					}
					break

				} else
				//时间与最新回复相同而最后回复人不同, 则是[同一贴同一秒内]的新回复, 不跳过.
				//不过说起来, 这时候`thread.LastReplyTime`应该都大于等于`nowFreshPostTime`的...
				//也就是说, 若`lastFreshPostTime != nowFreshPostTime`, 这一步是不必要做的.
				//往上两段的代码也是这个思路.
				//TODO: 简化逻辑的可能.
				if thread.LastReplyTime.Equal(lastFreshPostTime) &&
					lastFreshPostMap[thread.Tid] == thread.LastReplyer.ID {
					continue
				}
				if thread.LastReplyTime.Equal(nowFreshPostTime) {
					nowFreshPostMap[thread.Tid] = thread.LastReplyer.ID
				}

				//防止百度抽风导致没有用户名.
				if thread.LastReplyer.Name == "" {
					thread.LastReplyer.Name = TryGettingUserName(accWin8, thread.LastReplyer.ID)
				}
				freshThreadList = append(freshThreadList, thread)
			}

			if threadCount := len(freshThreadList); threadCount != 0 {
				if newRn := 8 + threadCount*2; newRn > 100 {
					monitor.forum_page_monitor.rn = 100
				} else {
					monitor.forum_page_monitor.rn = 8 + threadCount*2
				}

				monitor.PageChan <- ForumPage{
					Forum:      forumPage.Forum,
					ThreadList: freshThreadList,
					Extra:      forumPage.Extra,
				}

				lastFreshPostTime = nowFreshPostTime
				lastFreshPostMap = nowFreshPostMap

			}

		}
	}()

	return &monitor
}
