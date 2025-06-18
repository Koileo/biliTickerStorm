# 🎫 BiliTickerStorm - B 站分布式抢票

> 本项目使用 **Docker Swarm** 构建，具备良好的分布式扩展能力，可实现多节点协作式抢票。

---

## 📦 项目结构

```bash
.
├── docker-compose.yml            # 兼容 Compose 和 Swarm 的服务定义
├── master.Dockerfile             # ticket-master 构建文件
├── worker.Dockerfile             # ticket-worker 构建文件
├── python.Dockerfile             # gt-python 图形验证服务
├── data/                         # 配置数据目录（挂载给 master）
└── README.md
```

---

## ⚙️ 服务组件说明

| 服务名          | 描述                    | 备注       |
| --------------- | ----------------------- | ---------- |
| `ticket-master` | 主控服务，负责调度任务  | 单实例部署 |
| `ticket-worker` | 抢票 worker，可横向扩展 | 支持多实例 |
| `gt-python`     | 图形验证处理服务        | 单实例部署 |

---

## 🚀 快速部署步骤（Docker Swarm）

### 0. 下载 or Clone 本项目

### 1. 配置 Swarm 集群

> 本项目暂只支持单个 master 节点

参考 https://learn.microsoft.com/zh-cn/virtualization/windowscontainers/manage-containers/swarm-mode

linux 系统检查 vxlan 内核

```bash
lsmod | grep vxlan
sudo modprobe vxlan  # 如果没加载则加载
```

---

### 2. 部署服务

> 在 master 节点运行，可以在 docker-compose-swarm.ym 中更改相应配置

```bash
# 启动
docker stack deploy -c docker-compose-swarm.yml bili-ticker-storm
# 关闭
docker stack rm bili-ticker-storm
```

> `bili-ticker-storm` 是 Stack 名称，服务会注册为 `bili-ticker-storm_ticket-master` 等。

---

## 📂 配置说明

将抢票配置文件放置在 `data/` 目录下，会自动挂载至 master 容器 `/app/data`

抢票配置为 biliTickerBuy 生成的配置文件 https://github.com/mikumifa/biliTickerBuy

---

## 📌 环境变量

### ticket-worker 支持：

| 环境变量名          | 说明                 |
| ------------------- | -------------------- |
| `PUSHPLUS_TOKEN`    | plusplus 推送配置    |
| `TICKET_INTERVAL`   | 抢票间隔秒数（可选） |
| `TICKET_TIME_START` | 定时启动时间（可选） |

---

## 📄 License

[MIT License](LICENSE)
