package controllers

import (
	"github.com/gin-gonic/gin"

	"server/database"
	"server/models"
	"server/utils"
)

// PublicBanners 公开接口：返回启用中(status=1)的 banner 列表，按 sort 升序、id 降序。
// 供 C 端 App（uniapp H5 / agent-ui / client-pc-ui）首页轮播调用，无需登录。
func PublicBanners(c *gin.Context) {
	var list []models.Banner
	database.DB.Where("status = 1").Order("sort asc").Order("id desc").Find(&list)
	out := make([]gin.H, 0, len(list))
	for _, b := range list {
		out = append(out, gin.H{
			"id": b.ID, "title": b.Title, "image": b.Image,
			"link": b.Link, "sort": b.Sort,
		})
	}
	utils.OK(c, out)
}

// AdminBannerList 管理端 banner 列表（分页）
func AdminBannerList(c *gin.Context) {
	page, size := utils.Page(c)
	q := database.DB.Model(&models.Banner{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("title"); v != "" {
		q = q.Where("title LIKE ?", "%"+v+"%")
	}
	var total int64
	q.Count(&total)
	var list []models.Banner
	q.Order("sort asc").Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// AdminBannerSave 新增(id=0)/编辑 banner
func AdminBannerSave(c *gin.Context) {
	var req struct {
		ID     uint   `json:"id"`
		Title  string `json:"title" binding:"required"`
		Image  string `json:"image" binding:"required"`
		Link   string `json:"link"`
		Sort   int    `json:"sort"`
		Status int    `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请填写标题和图片")
		return
	}
	status := req.Status
	if req.ID == 0 && status == 0 {
		status = 1 // 仅新建且未指定时默认启用；编辑时尊重原状态
	}
	if req.ID == 0 {
		b := models.Banner{
			Title: req.Title, Image: req.Image, Link: req.Link,
			Sort: req.Sort, Status: status,
		}
		if err := database.DB.Create(&b).Error; err != nil {
			utils.ServerError(c, "新增失败")
			return
		}
		utils.OKMsg(c, "新增成功", b)
		return
	}
	updates := map[string]interface{}{
		"title": req.Title, "image": req.Image, "link": req.Link,
		"sort": req.Sort, "status": status,
	}
	if err := database.DB.Model(&models.Banner{}).Where("id = ?", req.ID).Updates(updates).Error; err != nil {
		utils.ServerError(c, "保存失败")
		return
	}
	utils.OKMsg(c, "保存成功", nil)
}

// AdminBannerStatus 启用/停用 banner
func AdminBannerStatus(c *gin.Context) {
	var req struct {
		ID     uint `json:"id" binding:"required"`
		Status int  `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if err := database.DB.Model(&models.Banner{}).Where("id = ?", req.ID).Update("status", req.Status).Error; err != nil {
		utils.ServerError(c, "操作失败")
		return
	}
	utils.OKMsg(c, "操作成功", nil)
}

// AdminBannerDelete 删除 banner
func AdminBannerDelete(c *gin.Context) {
	id := jsonID(c, "id")
	if err := database.DB.Delete(&models.Banner{}, id).Error; err != nil {
		utils.ServerError(c, "删除失败")
		return
	}
	utils.OKMsg(c, "删除成功", nil)
}