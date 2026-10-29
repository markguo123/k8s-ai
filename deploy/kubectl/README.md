# deploy/kubectl —— kubectl 离线投递目录（P6.3）

后端默认档镜像（仓库根 `Dockerfile`）运行阶段需要内置 `kubectl`。这里有两条投递路径，**离线投递优先**：

| 路径 | 怎么用 | 适用场景 |
| --- | --- | --- |
| A. 离线投递 | 把 `kubectl`（可选同名 `kubectl.sha256`，内容为裸 hash）放进本目录，直接 `docker build`，不需要任何 build-arg | 气隙/内网交付，构建全程零外网 |
| B. mirror 下载 | `--build-arg KUBECTL_VERSION=v1.28.13 --build-arg KUBECTL_DOWNLOAD_BASE=<mirror 根>` | 构建机能出网 |

本目录里的二进制被 `.gitignore` 忽略，只保留 `.gitkeep`：一是不该把 50MB 二进制提交进仓库，
二是 `Dockerfile` 用 BuildKit bind mount 读 `deploy/kubectl`，源目录必须存在（目录不存在构建会直接报错）。
构建阶段读到的是只读挂载，不会往这里写东西，也不会因为目录里有二进制而多出一层镜像。

## 取与集群匹配的 kubectl

先查集群次版本，再取同次版本的 kubectl（官方支持 ±1 次版本偏差，但与集群一致最省事）：

```bash
kubectl version -o json | jq -r .serverVersion.gitVersion   # 已有可用集群时
```

有外网的机器上下载并校验（`<ver>` 形如 `v1.28.13`）：

```bash
ver=v1.28.13
# 官方源
base=https://dl.k8s.io/release
# 国内无 dl.k8s.io 出口时改用它，目录结构与官方一致且提供裸 hash 的 .sha256
# base=https://files.m.daocloud.io/dl.k8s.io/release
curl -fsSLo kubectl        "$base/$ver/bin/linux/amd64/kubectl"
curl -fsSLo kubectl.sha256 "$base/$ver/bin/linux/amd64/kubectl.sha256"
sha256sum -c <(printf '%s  kubectl\n' "$(cat kubectl.sha256)")
```
内网自建 mirror 时，把 mirror 根指到与 `dl.k8s.io/release` 同样的目录结构
（`<base>/<版本>/bin/linux/<架构>/kubectl` 与同名 `.sha256`），走路径 B 即可，不必往本目录放文件。

## 构建后自查
```bash
docker run --rm --entrypoint /bin/bash <img> -lc \
  'id -u; command -v bash jq kubectl; kubectl version --client'
```
期望 uid 为 `65532`、三条命令路径都能打印出来，且构建日志里出现 `kubectl: OK`（校验通过）或
`kubectl 来自离线投递`。构建日志显示 `KUBECTL_VERSION 为空：跳过 kubectl 内置` 时，镜像里没有 kubectl，
kubectl 工具会返回可指导文案，只读链路（结构化只读工具与 scan 走 client-go）不受影响。
