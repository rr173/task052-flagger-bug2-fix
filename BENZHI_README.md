# task052-flagger 特性开关求值服务

本服务解决「进程内特性开关（feature flag）求值」的业务问题：维护一组命名开关，每个开关声明取值类型（布尔/数字/字符串）、默认值与有序目标规则列表，规则可附带百分比放量做确定性分桶。调用方携带求值上下文（属性映射）请求对某开关求值，服务按规则顺序求值并返回命中的取值、命中原因（「默认」或「规则-N」）与本次求值过程中最后一次百分比分桶值。所有状态保存在进程内存中，不依赖外部服务。

## 主要输入输出

- 输入：HTTP 接口（注册/查询/列举/删除开关、求值、统计），请求体为 JSON。
- 输出：JSON 响应，含取值、命中原因、是否命中、分桶值等字段。
- 自检：`--smoke-test` 执行内置自检后自行退出，不依赖外部服务。

## 标准本地命令

```bash
go build ./...          # 编译
go run .                # 启动（默认监听 :8080）
go run . --smoke-test   # 自检后退出
go test ./...           # 测试
```

## Benzhi 评测镜像构建

`build_benzhi_docker.sh` 接受两个参数：镜像名与平台。

```bash
# amd64
bash ./build_benzhi_docker.sh go-task-benzhi:amd64 linux/amd64
docker run -it go-task-benzhi:amd64:latest

# arm64
bash ./build_benzhi_docker.sh go-task-benzhi:arm64 linux/arm64
docker run -it go-task-benzhi:arm64:latest
```

构建后使用 `docker run -it <镜像名>` 进入容器 shell 进行操作。

## 工具链

- 本机 Go：go1.26.3（`GOTOOLCHAIN=local`，`go.mod` 语言版本 `go 1.26.3`）。
- 仅使用 Go 标准库，无第三方依赖。
- 构建设置 `CGO_ENABLED=0`。
