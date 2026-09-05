# SSH 转发管理器设计文档

## 概述

一个图形化 SSH 端口转发管理工具，支持一键连接/断开，自动重连，DNS 缓存清理。

## 技术选型

- **GUI 框架**: Gio（现代化、高性能、GPU 加速）
- **底层**: `os/exec` 调用 SSH 命令
- **配置存储**: 本地 JSON 文件
- **语言**: Go

## 功能需求

### 核心功能
1. 添加/编辑/删除转发规则
2. 一键连接/断开 SSH 隧道
3. 实时状态显示
4. 配置持久化（本地 JSON）

### 自动重连
- 检测连接中断
- 清空本地 DNS 缓存
- 重新解析域名获取新 IP
- 使用新 IP 自动重连

## 文件结构

```
port/
├── main.go           # 入口
├── ui.go             # Gio UI 界面
├── ssh.go            # SSH 连接逻辑
├── config.go         # 配置管理
├── go.mod
└── go.sum
```

## 数据结构

```go
type ForwardConfig struct {
    ID          string `json:"id"`
    Name        string `json:"name"`
    RemoteHost  string `json:"remote_host"`
    RemotePort  int    `json:"remote_port"`
    LocalHost   string `json:"local_host"`
    LocalPort   int    `json:"local_port"`
    SSHUser     string `json:"ssh_user"`
    AutoReconnect bool `json:"auto_reconnect"`
    MaxRetries  int    `json:"max_retries"`
    RetryInterval int  `json:"retry_interval"`
}

type AppState struct {
    Forwards    []ForwardConfig `json:"forwards"`
    ActiveConns map[string]*SSHConn `json:"-"`
}

type SSHConn struct {
    Config    ForwardConfig
    Process   *exec.Cmd
    Status    string
    CurrentIP string
}
```

## 界面设计

```
┌─────────────────────────────────────┐
│         SSH 转发管理器              │
├─────────────────────────────────────┤
│  转发名称: [我的服务器         ]    │
│  远程主机: [192.168.1.100     ]    │
│  远程端口: [8080             ]     │
│  本地主机: [localhost         ]    │
│  本地端口: [80                ]     │
│  SSH 用户: [admin            ]     │
│  自动重连: [✓]                     │
├─────────────────────────────────────┤
│  [添加] [编辑] [删除]              │
├─────────────────────────────────────┤
│  ┌─────────────────────────────┐   │
│  │ ☑ 我的服务器               │   │
│  │   192.168.1.100:8080→80    │   │
│  │   状态: 已连接 ✓           │   │
│  └─────────────────────────────┘   │
├─────────────────────────────────────┤
│      [▶ 连接]  [⏹ 断开]           │
└─────────────────────────────────────┘
```

## DNS 缓存清理

### Windows
```bash
ipconfig /flushdns
```

### Linux (systemd-resolved)
```bash
sudo systemd-resolve --flush-caches
```

### Linux (nscd)
```bash
sudo service nscd restart
```

## 自动重连流程

1. SSH 进程退出或连接中断
2. 清空本地 DNS 缓存
3. 重新解析域名获取新 IP
4. 对比新旧 IP（记录变化）
5. 使用新 IP 重新连接
6. 重试次数用尽后停止

## 测试计划

1. 基本连接/断开
2. 添加/编辑/删除规则
3. 配置保存/加载
4. 自动重连（模拟 IP 变化）
5. 跨平台测试（Windows + Linux）

## 依赖

- `gioui.org` - Gio GUI 框架
- `gioui.org/widget` - 组件
- `gioui.org/layout` - 布局
- `gioui.org/op` - 绘图操作
