package controllers

import (
	"fmt"
	"sort"
	"time"

	"github.com/gin-gonic/gin"

	"server/database"
	"server/models"
	"server/utils"
)

// AdminLatestEvents 最新业务动态通知（新注册/下单/平仓/充值/提现/代理提现/新代理）
// 聚合各业务表最近记录，按时间倒序返回，供总后台顶部消息通知轮询展示。
// 每个事件使用稳定 ID（类型+来源记录ID），前端据此持久化已读状态，刷新不重复提示。
func AdminLatestEvents(c *gin.Context) {
	type event struct {
		ID      string    `json:"id"`
		Type    string    `json:"type"`
		Title   string    `json:"title"`
		Content string    `json:"content"`
		Time    time.Time `json:"time"`
	}
	var evs []event
	appendEv := func(id, t, title, content string, at time.Time) {
		evs = append(evs, event{ID: id, Type: t, Title: title, Content: content, Time: at})
	}
	getUserName := func(id uint) string {
		var u models.User
		if database.DB.Select("username").First(&u, id).Error == nil {
			return u.Username
		}
		return ""
	}
	getProductName := func(id uint) string {
		var p models.Product
		if database.DB.Select("name").First(&p, id).Error == nil {
			return p.Name
		}
		return ""
	}
	getAgentName := func(id uint) string {
		var a models.Agent
		if database.DB.Select("agent_name").First(&a, id).Error == nil {
			return a.AgentName
		}
		return ""
	}

	// 新注册用户
	var users []models.User
	database.DB.Select("id,username,created_at").Order("id desc").Limit(20).Find(&users)
	for _, u := range users {
		appendEv("register:"+fmt.Sprint(u.ID), "register", "新用户注册", "用户 "+u.Username+" 注册成功", u.CreatedAt)
	}

	// 新开仓订单（不含系统生成的平仓记录）
	sideMap := map[int]string{1: "买涨", 2: "买跌"}
	var orders []models.Order
	database.DB.Select("id,user_id,product_id,side,volume,status,created_at").
		Where("is_close = 0").Order("id desc").Limit(20).Find(&orders)
	for _, o := range orders {
		name := getUserName(o.UserID)
		if name == "" {
			name = fmt.Sprintf("ID:%d", o.UserID)
		}
		appendEv("order:"+fmt.Sprint(o.ID), "order", "用户下单",
			fmt.Sprintf("%s 下单 %s %d 手", name, sideMap[o.Side], o.Volume), o.CreatedAt)
	}

	// 平仓（持仓已平仓）
	var closed []models.Position
	database.DB.Select("id,user_id,product_id,volume,profit,status,closed_at,updated_at").
		Where("status = 2").Order("id desc").Limit(20).Find(&closed)
	for _, p := range closed {
		name := getUserName(p.UserID)
		if name == "" {
			name = fmt.Sprintf("ID:%d", p.UserID)
		}
		prod := getProductName(p.ProductID)
		if prod == "" {
			prod = "品种"
		}
		at := p.UpdatedAt
		if p.ClosedAt != nil {
			at = *p.ClosedAt
		}
		appendEv("close:"+fmt.Sprint(p.ID), "close", "用户平仓",
			fmt.Sprintf("%s 平仓 %s %d 手，盈亏 %s", name, prod, p.Volume, p.Profit.String()), at)
	}

	// 实名认证申请（待后台审核）
	var realnameUsers []models.User
	database.DB.Select("id,username,real_name,updated_at").
		Where("real_name_status = 1").Order("id desc").Limit(20).Find(&realnameUsers)
	for _, u := range realnameUsers {
		appendEv("realname:"+fmt.Sprint(u.ID), "realname", "实名审核",
			"用户 "+u.Username+" 提交实名认证（"+u.RealName+"），待审核", u.UpdatedAt)
	}

	// 充值（按审核状态区分文案）
	var deposits []models.Deposit
	database.DB.Select("id,user_id,amount,status,created_at,audit_at").Order("id desc").Limit(20).Find(&deposits)
	for _, d := range deposits {
		name := getUserName(d.UserID)
		if name == "" {
			name = fmt.Sprintf("ID:%d", d.UserID)
		}
		switch d.Status {
		case 0:
			appendEv("deposit:"+fmt.Sprint(d.ID), "deposit", "充值审核",
				fmt.Sprintf("%s 充值 %s，待审核", name, d.Amount.String()), d.CreatedAt)
		case 1:
			at := d.CreatedAt
			if d.AuditAt != nil {
				at = *d.AuditAt
			}
			appendEv("deposit:"+fmt.Sprint(d.ID), "deposit", "充值到账",
				fmt.Sprintf("%s 充值 %s 已到账", name, d.Amount.String()), at)
		case 2:
			appendEv("deposit:"+fmt.Sprint(d.ID), "deposit", "充值被拒绝",
				fmt.Sprintf("%s 充值 %s 未通过", name, d.Amount.String()), d.CreatedAt)
		}
	}

	// 提现（按审核状态区分文案）
	var withdrawals []models.Withdraw
	database.DB.Select("id,user_id,amount,status,created_at,audit_at").Order("id desc").Limit(20).Find(&withdrawals)
	for _, w := range withdrawals {
		name := getUserName(w.UserID)
		if name == "" {
			name = fmt.Sprintf("ID:%d", w.UserID)
		}
		switch w.Status {
		case 0:
			appendEv("withdraw:"+fmt.Sprint(w.ID), "withdraw", "提现审核",
				fmt.Sprintf("%s 申请提现 %s，待审核", name, w.Amount.String()), w.CreatedAt)
		case 1:
			at := w.CreatedAt
			if w.AuditAt != nil {
				at = *w.AuditAt
			}
			appendEv("withdraw:"+fmt.Sprint(w.ID), "withdraw", "提现已通过",
				fmt.Sprintf("%s 提现 %s 已通过", name, w.Amount.String()), at)
		case 2:
			appendEv("withdraw:"+fmt.Sprint(w.ID), "withdraw", "提现被拒绝",
				fmt.Sprintf("%s 提现 %s 未通过", name, w.Amount.String()), w.CreatedAt)
		}
	}

	// 代理提现
	var aw []models.AgentWithdraw
	database.DB.Select("id,agent_id,amount,status,created_at").Order("id desc").Limit(20).Find(&aw)
	for _, a := range aw {
		name := getAgentName(a.AgentID)
		if name == "" {
			name = fmt.Sprintf("代理ID:%d", a.AgentID)
		}
		appendEv("agent_withdraw:"+fmt.Sprint(a.ID), "agent_withdraw", "代理商提现", fmt.Sprintf("%s 申请提现 %s", name, a.Amount.String()), a.CreatedAt)
	}

	// 新代理
	var agents []models.Agent
	database.DB.Select("id,agent_name,created_at").Order("id desc").Limit(20).Find(&agents)
	for _, a := range agents {
		appendEv("agent:"+fmt.Sprint(a.ID), "agent", "新增代理商", "代理商 "+a.AgentName+" 入驻", a.CreatedAt)
	}

	// 按时间倒序
	sort.Slice(evs, func(i, j int) bool { return evs[i].Time.After(evs[j].Time) })
	if len(evs) > 50 {
		evs = evs[:50]
	}
	utils.OK(c, evs)
}