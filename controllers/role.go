package controllers

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"server/cache"
	"server/database"
	"server/models"
	"server/utils"
)

// RoleReq 角色新增/编辑
type RoleReq struct {
	ID     uint   `json:"id"`
	Name   string `json:"name" binding:"required"`
	Code   string `json:"code" binding:"required"`
	Remark string `json:"remark"`
	Status int    `json:"status"`
}

// RoleList 分页查询角色
func RoleList(c *gin.Context) {
	page, size := utils.Page(c)
	keyword := c.Query("keyword")

	q := database.DB.Model(&models.Role{})
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("name LIKE ? OR code LIKE ?", like, like)
	}
	var total int64
	q.Count(&total)
	var list []models.Role
	q.Order("id asc").Offset((page - 1) * size).Limit(size).Find(&list)
	utils.OK(c, utils.PageData{List: list, Total: total, Page: page, Size: size})
}

// RoleAll 全部角色（下拉）
func RoleAll(c *gin.Context) {
	var list []models.Role
	database.DB.Where("status = 1").Find(&list)
	utils.OK(c, list)
}

// RoleSave 新增/编辑角色
func RoleSave(c *gin.Context) {
	var req RoleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	var count int64
	database.DB.Model(&models.Role{}).
		Where("code = ? AND id <> ?", req.Code, req.ID).Count(&count)
	if count > 0 {
		utils.BadRequest(c, "角色编码已存在")
		return
	}

	if req.ID == 0 {
		role := models.Role{Name: req.Name, Code: req.Code, Remark: req.Remark, Status: req.Status}
		if role.Status == 0 {
			role.Status = 1
		}
		if err := database.DB.Create(&role).Error; err != nil {
			utils.ServerError(c, "新增失败")
			return
		}
		utils.OKMsg(c, "新增成功", role)
		return
	}
	var role models.Role
	if err := database.DB.First(&role, req.ID).Error; err != nil {
		utils.BadRequest(c, "角色不存在")
		return
	}
	if role.IsSuper == 1 {
		utils.BadRequest(c, "内置超级角色不可修改")
		return
	}
	role.Name = req.Name
	role.Code = req.Code
	role.Remark = req.Remark
	if err := database.DB.Save(&role).Error; err != nil {
		utils.ServerError(c, "更新失败")
		return
	}
	utils.OKMsg(c, "编辑成功", role)
}

// RoleStatus 启用/禁用
func RoleStatus(c *gin.Context) {
	var req struct {
		ID     uint `json:"id" binding:"required"`
		Status int  `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	var role models.Role
	if err := database.DB.First(&role, req.ID).Error; err == nil && role.IsSuper == 1 {
		utils.BadRequest(c, "内置超级角色不可禁用")
		return
	}
	if err := database.DB.Model(&models.Role{}).
		Where("id = ?", req.ID).Update("status", req.Status).Error; err != nil {
		utils.ServerError(c, "操作失败")
		return
	}
	utils.OKMsg(c, "操作成功", nil)
}

// RoleDelete 删除角色
func RoleDelete(c *gin.Context) {
	id := c.Param("id")
	var role models.Role
	if err := database.DB.First(&role, id).Error; err != nil {
		utils.BadRequest(c, "角色不存在")
		return
	}
	if role.IsSuper == 1 {
		utils.BadRequest(c, "内置超级角色不可删除")
		return
	}
	var used int64
	database.DB.Model(&models.Admin{}).Where("role_id = ?", id).Count(&used)
	if used > 0 {
		utils.BadRequest(c, "该角色下存在管理员，无法删除")
		return
	}
	database.DB.Delete(&models.Role{}, id)
	database.DB.Where("role_id = ?", id).Delete(&models.RoleMenu{})
	// 角色删除：清除全部角色的权限码缓存（前缀批量）
	cache.InvalidatePerms()
	utils.OKMsg(c, "删除成功", nil)
}

// RoleAssignMenus 给角色分配菜单权限
func RoleAssignMenus(c *gin.Context) {
	var req struct {
		RoleID  uint   `json:"roleId" binding:"required"`
		MenuIDs []uint `json:"menuIds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	var role models.Role
	if err := database.DB.First(&role, req.RoleID).Error; err != nil {
		utils.BadRequest(c, "角色不存在")
		return
	}
	if role.IsSuper == 1 {
		utils.OKMsg(c, "超级角色默认拥有全部权限", nil)
		return
	}
	// 事务：先删旧，再插新
	database.DB.Where("role_id = ?", req.RoleID).Delete(&models.RoleMenu{})
	if len(req.MenuIDs) > 0 {
		rows := make([]models.RoleMenu, 0, len(req.MenuIDs))
		for _, mid := range req.MenuIDs {
			rows = append(rows, models.RoleMenu{RoleID: req.RoleID, MenuID: mid})
		}
		database.DB.Create(&rows)
	}
	// 角色-菜单绑定变更：失效该角色权限码缓存（鉴权 Perm 热路径立即生效）
	cache.InvalidatePerms()
	utils.OKMsg(c, "授权成功", nil)
}

// RoleMenus 获取角色已分配的菜单 ID
func RoleMenus(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 32)
	ids := database.GetRoleMenuIDs(uint(id))
	utils.OK(c, ids)
}