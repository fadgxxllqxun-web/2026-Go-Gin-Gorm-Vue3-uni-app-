package controllers

import (
	"time"

	"github.com/gin-gonic/gin"

	"server/database"
	"server/middleware"
	"server/models"
	"server/utils"
)

// LoginReq 登录请求
type LoginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login 管理员登录
func Login(c *gin.Context) {
	var req LoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "请输入账号和密码")
		return
	}

	var admin models.Admin
	if err := database.DB.Where("username = ?", req.Username).First(&admin).Error; err != nil {
		utils.BadRequest(c, "账号不存在")
		return
	}
	if admin.Status != 1 {
		utils.BadRequest(c, "该账号已被禁用")
		return
	}
	if !database.CheckPwd(admin.Password, req.Password) {
		utils.BadRequest(c, "密码错误")
		return
	}

	// 角色信息
	var role models.Role
	isSuper := false
	if admin.RoleID > 0 {
		if err := database.DB.First(&role, admin.RoleID).Error; err == nil && role.IsSuper == 1 {
			isSuper = true
		}
	}

	// 刷新最后登录时间
	database.DB.Model(&admin).Update("last_login", time.Now())

	token, err := utils.SignToken(admin.ID, admin.Username, admin.RoleID, isSuper)
	if err != nil {
		utils.ServerError(c, "生成令牌失败")
		return
	}

	// 菜单 & 权限
	var menuTree []*models.Menu
	if isSuper {
		menuTree = database.GetAllMenuTree()
	} else {
		menuTree = database.GetRoleMenuTree(admin.RoleID)
	}
	perms := make([]string, 0)
	if isSuper {
		database.DB.Model(&models.Menu{}).Where("perms <> ''").Pluck("perms", &perms)
	} else {
		for p := range database.GetRolePermCodes(admin.RoleID) {
			perms = append(perms, p)
		}
	}

	utils.OK(c, gin.H{
		"token": token,
		"admin": admin,
		"role":  role,
		"menus": menuTree,
		"perms": perms,
	})
}

// Info 当前登录用户信息
func Info(c *gin.Context) {
	claims := middleware.GetClaims(c)
	var admin models.Admin
	if err := database.DB.Preload("Role").First(&admin, claims.AdminID).Error; err != nil {
		utils.Unauthorized(c)
		return
	}
	var menuTree []*models.Menu
	perms := make([]string, 0)
	if claims.IsSuper {
		menuTree = database.GetEnabledMenuTree(true, 0)
		database.DB.Model(&models.Menu{}).Where("perms <> ''").Pluck("perms", &perms)
	} else {
		menuTree = database.GetEnabledMenuTree(false, admin.RoleID)
		for p := range database.GetRolePermCodes(admin.RoleID) {
			perms = append(perms, p)
		}
	}
	utils.OK(c, gin.H{
		"admin": admin,
		"role":  admin.Role,
		"menus": menuTree,
		"perms": perms,
	})
}

// Menus 当前用户菜单
func Menus(c *gin.Context) {
	claims := middleware.GetClaims(c)
	utils.OK(c, database.GetEnabledMenuTree(claims.IsSuper, claims.RoleID))
}

// Logout 退出登录
func Logout(c *gin.Context) {
	utils.OKMsg(c, "已退出登录", nil)
}

// ChangePassword 修改密码
func ChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"oldPassword" binding:"required"`
		NewPassword string `json:"newPassword" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	adminID := middleware.GetAdminID(c)
	var admin models.Admin
	if err := database.DB.First(&admin, adminID).Error; err != nil {
		utils.Unauthorized(c)
		return
	}
	if !database.CheckPwd(admin.Password, req.OldPassword) {
		utils.BadRequest(c, "原密码错误")
		return
	}
	newHash, err := database.Hash(req.NewPassword)
	if err != nil {
		utils.ServerError(c, "加密失败")
		return
	}
	if err := database.DB.Model(&admin).Update("password", newHash).Error; err != nil {
		utils.ServerError(c, "修改失败")
		return
	}
	utils.OKMsg(c, "修改成功", nil)
}

// ProfileUpdate 当前用户更新个人资料（头像/昵称/邮箱/手机号）
func ProfileUpdate(c *gin.Context) {
	var req struct {
		Avatar   string `json:"avatar"`
		Nickname string `json:"nickname"`
		Email    string `json:"email"`
		Mobile   string `json:"mobile"`
	}
	_ = c.ShouldBindJSON(&req)

	adminID := middleware.GetAdminID(c)
	updates := map[string]interface{}{}
	if req.Avatar != "" {
		updates["avatar"] = req.Avatar
	}
	if req.Nickname != "" {
		updates["nickname"] = req.Nickname
	}
	if req.Email != "" {
		updates["email"] = req.Email
	}
	if req.Mobile != "" {
		updates["mobile"] = req.Mobile
	}
	if len(updates) == 0 {
		utils.BadRequest(c, "没有需要更新的内容")
		return
	}
	if err := database.DB.Model(&models.Admin{}).Where("id = ?", adminID).Updates(updates).Error; err != nil {
		utils.ServerError(c, "更新失败")
		return
	}
	var admin models.Admin
	if err := database.DB.First(&admin, adminID).Error; err != nil {
		utils.ServerError(c, "查询失败")
		return
	}
	utils.OKMsg(c, "更新成功", admin)
}