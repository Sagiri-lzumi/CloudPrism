// Package config 解析启动配置（程序目录旁 data/config.json）。
//
// 定位：**只承载「进程启动前必须知道」的参数** —— 监听端口与顺延范围。
// 监听地址刻意不在此列（见 Config 下方说明）。这些值必须在 web.New/Listen
// 之前就确定，无法放进
// 运行期设置存储（cloudprism_settings.json 由 appstate 持有，Web 界面
// 改完要重启才生效，且改不动已经绑好的监听）。其余运行期设置一律仍走
// pkg/settings，不在这里重复。
//
// 容错原则与 pkg/settings.Open 一致：文件不存在 / 损坏 / 字段非法
// 一律回退默认值，**绝不阻塞启动** —— 一个手滑写错的 config.json
// 不该让用户完全打不开程序。回退情况经 Problems 报告给调用方写日志。
package config

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

// 默认值。历史上这些常量硬编码在 main.go，现统一收在这里。
const (
	// DefaultPort 默认监听端口（历史值 7840，保持不变）。
	DefaultPort = 7840
	// DefaultHost 默认监听地址：仅本机。
	// 局域网档开启时由 main.go 改为 0.0.0.0（见 listen/lan 设置）。
	DefaultHost = "127.0.0.1"
	// DefaultPortRange 默认顺延范围：DefaultPort 起共这么多个端口。
	DefaultPortRange = 10
	// maxPortRange 顺延范围上限，防配置写出一个扫满全端口的离谱值。
	maxPortRange = 256
)

// Config 是启动配置。零值非空时均应先经 Load 填充默认值。
type Config struct {
	// Port 起始监听端口。被占用时向上顺延，见 PortRange。
	Port int
	// PortRange 顺延端口个数（含起始端口），即尝试
	// Port .. Port+PortRange-1。
	PortRange int
}

// 注意：配置刻意**不含监听地址**。监听地址与「远端是否鉴权」是同一条安全
// 决策的两面，必须由 listen/lan 开关统一裁决（见 main.go）；若配置能单方面
// 指定 host，就会出现「绑了 0.0.0.0 但令牌闸门未启用」的自相矛盾状态。
// 历史上 config.json 曾有 host 字段，但从未真正生效（只会产生一条 WARN），
// 已按「死配置面即删」的原则移除；文件里残留的 host 键会被静默忽略。

// Default 返回全默认配置。
func Default() Config {
	return Config{Port: DefaultPort, PortRange: DefaultPortRange}
}

// fileShape 是 config.json 的线上结构。
//
// 用指针接收可选字段，以区分「没写这一项」与「显式写了 0」：
// 前者沿用默认值，后者（0 端口）视为非法同样回退默认，并记入 Problems。
type fileShape struct {
	Port      *int `json:"port"`
	PortRange *int `json:"port_range"`
}

// Load 从 path 载入配置。
//
// 返回的 Config 保证字段全部有效（非法项已回退默认值）；Problems 列出
// 每一条回退原因，供调用方写进启动日志 —— 静默吞掉用户写错的配置会让
// 「我明明配了 8080 怎么还是 7840」无从排查。
//
// path 为空时返回 Default() 与 nil（无配置文件即全默认，不算异常）。
func Load(path string) (Config, []string) {
	cfg := Default()
	if strings.TrimSpace(path) == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // 无配置文件是正常情况，静默用默认值
		}
		return cfg, []string{"读取配置文件失败，已用默认值：" + err.Error()}
	}

	// 去掉 UTF-8 BOM：Windows 记事本另存 UTF-8 默认带 BOM，而
	// encoding/json 见到 BOM 会直接报 "invalid character '\ufeff'"。
	// 不剥掉的话，用户用记事本改端口 → 配置静默失效 → 极难自己定位。
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	var shape fileShape
	if err := json.Unmarshal(data, &shape); err != nil {
		return cfg, []string{"配置文件不是合法 JSON，已用默认值：" + err.Error()}
	}

	var problems []string
	if shape.Port != nil {
		if p := *shape.Port; p >= 1 && p <= 65535 {
			cfg.Port = p
		} else {
			problems = append(problems, "port 超出 1-65535 范围，已用默认值 "+strconv.Itoa(DefaultPort))
		}
	}
	if shape.PortRange != nil {
		switch r := *shape.PortRange; {
		case r >= 1 && r <= maxPortRange:
			cfg.PortRange = r
		case r > maxPortRange:
			cfg.PortRange = maxPortRange
			problems = append(problems, "port_range 超过上限，已截断为 "+strconv.Itoa(maxPortRange))
		default:
			problems = append(problems, "port_range 过小，已用默认值 "+strconv.Itoa(DefaultPortRange))
		}
	}

	// 端口顺延不能越过 65535，否则 net.Listen 会拿到非法端口。
	if cfg.Port+cfg.PortRange-1 > 65535 {
		room := 65536 - cfg.Port
		if room < 1 {
			room = 1
		}
		cfg.PortRange = room
		problems = append(problems, "port + port_range 越界，已将 port_range 收窄为 "+strconv.Itoa(room))
	}

	return cfg, problems
}

// Ports 返回按尝试顺序排列的候选端口列表
// （Port, Port+1, ... Port+PortRange-1）。
//
// 实际监听（listenOn）按此顺序顺延；非主实例找出已有实例界面时的探测
// 范围是它的超集（见 main.go mergePorts），两者方向相反、互不构成约束。
func (c Config) Ports() []int {
	n := c.PortRange
	if n < 1 {
		n = 1
	}
	ports := make([]int, 0, n)
	for i := 0; i < n; i++ {
		p := c.Port + i
		if p > 65535 {
			break
		}
		ports = append(ports, p)
	}
	return ports
}
