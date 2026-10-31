package controllers

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"server/database"
	"server/models"
	"server/services/notify"
	"server/utils"
)

// UserNotices 用户消息列表（只返回发给当前用户的定向公告；全局公告 user_id=0 不广播给用户）
func UserNotices(c *gin.Context) {
	user := middlewareGetUser(c)
	page, size := utils.Page(c)
	q := database.DB.Model(&models.Notice{}).
		Where("user_id = ?", user.ID)
	if v := c.Query("type"); v != "" {
		q = q.Where("type = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []models.Notice
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// UserNoticeUnread 未读消息数
func UserNoticeUnread(c *gin.Context) {
	user := middlewareGetUser(c)
	var n int64
	database.DB.Model(&models.Notice{}).
		Where("user_id = ? AND is_read = 0", user.ID).Count(&n)
	utils.OK(c, gin.H{"unread": n})
}

// UserNoticeRead 标记已读（ids 为空则全部标记）
func UserNoticeRead(c *gin.Context) {
	user := middlewareGetUser(c)
	var req struct {
		Ids []uint `json:"ids"`
	}
	_ = c.ShouldBindJSON(&req)
	if len(req.Ids) == 0 {
		database.DB.Model(&models.Notice{}).
			Where("user_id = ? AND is_read = 0", user.ID).
			Update("is_read", 1)
	} else {
		database.DB.Model(&models.Notice{}).
			Where("id IN ? AND user_id = ?", req.Ids, user.ID).
			Update("is_read", 1)
	}
	utils.OKMsg(c, "操作成功", nil)
}

// AdminNoticeList 管理端公告列表（通知管理页）
func AdminNoticeList(c *gin.Context) {
	page, size := utils.Page(c)
	q := database.DB.Model(&models.Notice{})
	if v := c.Query("type"); v != "" {
		q = q.Where("type = ?", v)
	}
	if v := c.Query("userId"); v != "" {
		q = q.Where("user_id = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []models.Notice
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	// 附带目标用户名（展示用）
	rows := make([]gin.H, 0, len(list))
	for _, n := range list {
		target := "全体"
		if n.UserID > 0 {
			var u models.User
			if database.DB.Select("username").First(&u, n.UserID).Error == nil {
				target = u.Username
			} else {
				target = "用户#" + fmt.Sprint(n.UserID)
			}
		}
		rows = append(rows, gin.H{
			"id": n.ID, "userId": n.UserID, "target": target,
			"type": n.Type, "title": n.Title, "content": n.Content,
			"isRead": n.IsRead, "createdAt": n.CreatedAt,
		})
	}
	utils.OK(c, utils.PageData{List: rows, Total: total, Page: page, Size: size})
}

// AdminNoticeCreate 发布公告（必须指定目标用户 userId>0，或 userId=0 表示"全体"）
func AdminNoticeCreate(c *gin.Context) {
	var req struct {
		UserID  uint   `json:"userId"`
		Type    string `json:"type"`
		Title   string `json:"title" binding:"required"`
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请填写标题和内容")
		return
	}
	typ := req.Type
	if typ == "" {
		typ = "bulletin"
	}
	notify.Create(req.UserID, typ, req.Title, req.Content)
	utils.OKMsg(c, "发布成功", nil)
}

// AdminNoticeDelete 删除公告
func AdminNoticeDelete(c *gin.Context) {
	id := middlewareGet(c, "id")
	if err := database.DB.Delete(&models.Notice{}, id).Error; err != nil {
		utils.ServerError(c, "删除失败")
		return
	}
	utils.OKMsg(c, "删除成功", nil)
}

func middlewareGet(c *gin.Context, key string) uint {
	return jsonID(c, key)
}