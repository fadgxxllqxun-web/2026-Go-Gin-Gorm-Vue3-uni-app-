package models

import "time"

// Admin 管理员/用户
type Admin struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Username  string    `gorm:"size:64;uniqueIndex;not null" json:"username"`
	Password  string    `gorm:"size:255;not null" json:"-"`
	Nickname  string    `gorm:"size:64" json:"nickname"`
	Avatar    string    `gorm:"size:255" json:"avatar"`
	Email     string    `gorm:"size:128" json:"email"`
	Mobile    string    `gorm:"size:32" json:"mobile"`
	RoleID    uint      `gorm:"default:0" json:"roleId"`
	Status    int       `gorm:"default:1" json:"status"` // 1 启用 0 禁用
	LastLogin *time.Time `json:"lastLogin"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Role      *Role     `gorm:"foreignKey:RoleID" json:"role"`
}

// Role 角色
type Role struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	Code      string    `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Remark    string    `gorm:"size:255" json:"remark"`
	IsSuper   int       `gorm:"default:0" json:"isSuper"` // 1 超级角色，拥有全部权限
	Status    int       `gorm:"default:1" json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RoleMenu 角色-菜单 多对多
type RoleMenu struct {
	RoleID uint `gorm:"primaryKey"`
	MenuID uint `gorm:"primaryKey"`
}

// Menu 菜单 / 权限
type Menu struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ParentID  uint      `gorm:"default:0;index" json:"parentId"`
	Name      string    `gorm:"size:64" json:"name"` // 路由 name
	Title     string    `gorm:"size:64;not null" json:"title"`
	Path      string    `gorm:"size:128" json:"path"`
	Component string    `gorm:"size:255" json:"component"`
	Icon      string    `gorm:"size:64" json:"icon"`
	Perms     string    `gorm:"size:128" json:"perms"` // 权限标识，如 sys:admin:list
	Type      string    `gorm:"size:32;default:C" json:"type"` // M 目录 C 菜单 F 按钮
	Sort      int       `gorm:"default:0" json:"sort"`
	Visible   int       `gorm:"default:1" json:"visible"` // 1 显示 0 隐藏
	Status    int       `gorm:"default:1" json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Children  []*Menu   `gorm:"-" json:"children"`
}

// OperationLog 操作日志
type OperationLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	AdminID   uint      `json:"adminId"`
	AdminName string    `gorm:"size:64" json:"adminName"`
	Module    string    `gorm:"size:64" json:"module"`
	Action    string    `gorm:"size:64" json:"action"`
	Method    string    `gorm:"size:16" json:"method"`
	Path      string    `gorm:"size:255" json:"path"`
	IP        string    `gorm:"size:64" json:"ip"`
	Status    int       `json:"status"`
	Latency   int64     `json:"latency"` // 毫秒
	ErrorMsg  string    `gorm:"size:255" json:"errorMsg"`
	CreatedAt time.Time `json:"createdAt"`
}

// Setting 系统设置 key-value，按分组存储
type Setting struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	GroupCode   string    `gorm:"size:32;index" json:"groupCode"` // oss / email / sms / website
	GroupName   string    `gorm:"size:64" json:"groupName"`
	Key         string    `gorm:"size:64" json:"key"`
	Value       string    `gorm:"type:text" json:"value"`
	Description string    `gorm:"size:255" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}