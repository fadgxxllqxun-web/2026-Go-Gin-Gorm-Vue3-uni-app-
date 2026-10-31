package config

import (
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

// Server 服务配置
type Server struct {
	Port      string `yaml:"port"`       // 监听端口
	Mode      string `yaml:"mode"`       // gin 运行模式: debug / release / test
	ApiPrefix string `yaml:"apiPrefix"`  // 接口前缀，默认 /api
}

// Database MySQL 配置
type Database struct {
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Name     string `yaml:"name"`
	// 连接池（百万并发承载关键：显式上限防打爆 MySQL max_connections）
	MaxOpenConns     int `yaml:"maxOpenConns"`     // 最大打开连接数（0=不限）
	MaxIdleConns     int `yaml:"maxIdleConns"`     // 最大空闲连接数
	ConnMaxLifetimeS int `yaml:"connMaxLifetimeS"` // 连接最大存活秒数（0=不限）
}

// JWT 令牌配置
type JWT struct {
	Secret   string `yaml:"secret"`
	TTLHours int    `yaml:"ttlHours"`
}

// Upload 文件上传配置
type Upload struct {
	Dir string `yaml:"dir"` // 上传目录（同时以 /static 静态服务）
}

// Cors 跨域配置
type Cors struct {
	Enabled          bool     `yaml:"enabled"`          // 是否启用跨域
	AllowedOrigins   []string `yaml:"allowedOrigins"`   // 允许的来源，["*"] 表示全部
	AllowedMethods   []string `yaml:"allowedMethods"`   // 允许的请求方法
	AllowedHeaders   []string `yaml:"allowedHeaders"`   // 允许的请求头
	AllowCredentials bool     `yaml:"allowCredentials"` // 是否允许携带凭证
	MaxAge           int      `yaml:"maxAge"`           // 预检缓存秒数
}

// Redis 缓存配置（行情/K线/tick 等热数据，重启不丢）
type Redis struct {
	Enabled  bool   `yaml:"enabled"`  // 是否启用；关闭或连接失败时降级为纯内存
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	PoolSize int    `yaml:"poolSize"`
}

// Config 总配置
type Config struct {
	Server   Server   `yaml:"server"`
	Database Database `yaml:"database"`
	Redis    Redis    `yaml:"redis"`
	JWT      JWT      `yaml:"jwt"`
	Upload   Upload   `yaml:"upload"`
	Cors     Cors     `yaml:"cors"`
}

var Cfg *Config

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load 从 config.yaml 加载配置（可用环境变量 CONFIG_FILE 指定路径）
func Load() *Config {
	// 内置默认值兜底
	Cfg = &Config{
		Server:   Server{Port: "8080", Mode: "debug", ApiPrefix: "/api"},
		Database: Database{Host: "127.0.0.1", Port: "3306", User: "gox", Password: "123456", Name: "gox", MaxOpenConns: 50, MaxIdleConns: 10, ConnMaxLifetimeS: 3600},
		Redis:    Redis{Enabled: true, Host: "127.0.0.1", Port: "6379", DB: 0, PoolSize: 20},
		JWT:      JWT{Secret: "ease-ui-server-secret-change-me", TTLHours: 24},
		Upload:   Upload{Dir: "uploads/static"},
		Cors: Cors{
			Enabled:          true,
			AllowedOrigins:   []string{"*"},
			AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
			AllowedHeaders:   []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With", "x-token"},
			AllowCredentials: true,
			MaxAge:           86400,
		},
	}

	path := getEnv("CONFIG_FILE", "config.yaml")
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, Cfg); err != nil {
			log.Printf("[config] 解析 %s 失败: %v，使用默认/环境变量配置", path, err)
		} else {
			log.Printf("[config] 已加载配置文件 %s", path)
		}
	} else {
		log.Printf("[config] 未找到 %s，使用默认/环境变量配置", path)
	}

	// 环境变量覆盖（优先级最高）
	if v := getEnv("PORT", ""); v != "" {
		Cfg.Server.Port = v
	}
	if v := getEnv("GIN_MODE", ""); v != "" {
		Cfg.Server.Mode = v
	}
	if v := getEnv("DB_HOST", ""); v != "" {
		Cfg.Database.Host = v
	}
	if v := getEnv("DB_PORT", ""); v != "" {
		Cfg.Database.Port = v
	}
	if v := getEnv("DB_USER", ""); v != "" {
		Cfg.Database.User = v
	}
	if v := getEnv("DB_PASSWORD", ""); v != "" {
		Cfg.Database.Password = v
	}
	if v := getEnv("DB_NAME", ""); v != "" {
		Cfg.Database.Name = v
	}
	if v := getEnv("REDIS_HOST", ""); v != "" {
		Cfg.Redis.Host = v
	}
	if v := getEnv("REDIS_PORT", ""); v != "" {
		Cfg.Redis.Port = v
	}
	if v := getEnv("REDIS_PASSWORD", ""); v != "" {
		Cfg.Redis.Password = v
	}
	if v := getEnv("REDIS_DISABLED", ""); v == "1" || v == "true" {
		Cfg.Redis.Enabled = false
	}
	if v := getEnv("JWT_SECRET", ""); v != "" {
		Cfg.JWT.Secret = v
	}
	if v := getEnv("UPLOAD_DIR", ""); v != "" {
		Cfg.Upload.Dir = v
	}

	applyDefaults(Cfg)
	log.Printf("[config] server=%s mode=%s db=%s:%s/%s upload=%s",
		Cfg.Server.Port, Cfg.Server.Mode, Cfg.Database.Host, Cfg.Database.Port, Cfg.Database.Name, Cfg.Upload.Dir)
	return Cfg
}

func applyDefaults(c *Config) {
	if c.Server.Port == "" {
		c.Server.Port = "8080"
	}
	if c.Server.Mode == "" {
		c.Server.Mode = "debug"
	}
	if c.Server.ApiPrefix == "" {
		c.Server.ApiPrefix = "/api"
	}
	if c.Database.Host == "" {
		c.Database.Host = "127.0.0.1"
	}
	if c.Database.Port == "" {
		c.Database.Port = "3306"
	}
	if c.Database.User == "" {
		c.Database.User = "gox"
	}
	if c.Database.Password == "" {
		c.Database.Password = "123456"
	}
	if c.Database.Name == "" {
		c.Database.Name = "gox"
	}
	if c.Database.MaxOpenConns <= 0 {
		c.Database.MaxOpenConns = 50
	}
	if c.Database.MaxIdleConns <= 0 {
		c.Database.MaxIdleConns = 10
	}
	if c.Database.ConnMaxLifetimeS <= 0 {
		c.Database.ConnMaxLifetimeS = 3600
	}
	if c.JWT.Secret == "" {
		c.JWT.Secret = "ease-ui-server-secret-change-me"
	}
	if c.JWT.TTLHours == 0 {
		c.JWT.TTLHours = 24
	}
	if c.Upload.Dir == "" {
		c.Upload.Dir = "uploads/static"
	}
	if len(c.Cors.AllowedOrigins) == 0 {
		c.Cors.AllowedOrigins = []string{"*"}
	}
	if len(c.Cors.AllowedMethods) == 0 {
		c.Cors.AllowedMethods = []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}
	}
	if len(c.Cors.AllowedHeaders) == 0 {
		c.Cors.AllowedHeaders = []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With", "x-token"}
	}
	if c.Cors.MaxAge == 0 {
		c.Cors.MaxAge = 86400
	}
}

// DSN 返回 MySQL 连接串
func (c *Config) DSN() string {
	// 超时参数防止慢查询/死连接拖垮连接池（批量 persist 场景尤为重要）
	dsn := c.Database.User + ":" + c.Database.Password +
		"@tcp(" + c.Database.Host + ":" + c.Database.Port + ")/" + c.Database.Name +
		"?charset=utf8mb4&parseTime=True&loc=Local" +
		"&timeout=5s&readTimeout=30s&writeTimeout=30s&interpolateParams=true"
	return dsn
}