# 规则来源与格式

本文讲分流项背后的规则数据：sing-box 能直接用什么，客户端怎样把各种格式统一处理，支持哪些格式和怎样转换，下载、镜像和更新，以及内置的规则目录。

不在本文：添加分流项、粘贴链接的界面和预览见 [用户体验](experience.md#3-添加分流项)；内置推荐方案选了哪些规则集见 [方案](plans.md#5-内置推荐方案)；编译好的规则集怎样写进内核配置、agent 的资源上限见 [生成配置](config.md)。

## 1. sing-box 本身支持什么

sing-box 1.14.4 的规则集只支持两种格式：`source`（JSON）和 `binary`（.srs）。自带的转换工具只能处理 AdGuard 列表。Clash 的 yaml/list、mihomo 的 .mrs、Surge 的 list、geosite.dat 都不能直接使用。所以**格式支持全部由客户端负责**。

## 2. 统一的处理流程

所有来源都走同一条流程：

1. **下载**：见第 5 节。
2. **识别格式**：看内容，不依赖扩展名。
3. **解析**：解析成内部的统一条目列表。
4. **分成两部分**：域名部分（域名 / 后缀 / 关键字 / 正则，以及进程、端口等不需要先解析域名的条件）和 IP 部分。
5. **合并编译**：把同一分流项的所有来源合并，并编译成内核支持的 .srs 版本。每个分流项最多生成两个资源，一个域名部分、一个 IP 部分。
6. **存储并交给 agent**：编译结果按内容寻址存储，作为 `res:` 资源交给 agent。

这条流程同时解决了三个问题：

- **格式差异。**
- **.srs 版本兼容。** 读入任何版本，都按当前内核的版本重新写出。
- **资源数量上限。** 一个订阅方案里的 47 个规则集，合并成 17 个分流项之后，最多 34 个资源。

内部条目列表也保存在客户端，供"测试网址"做本地匹配。

## 3. 支持的格式

| 格式 | 识别方式 | 处理 |
|---|---|---|
| sing-box .srs | 文件头 `SRS` | 读取后重新编译 |
| sing-box JSON（source） | 含 `version` 和 `rules` | 解析 headless rule，包括 logical 规则 |
| Clash rule-provider YAML | 含 `payload` | 按 behavior 处理：domain / ipcidr / classical。行为未知时根据内容推断 |
| Clash / Surge 文本列表 | 每行 `TYPE,value[,…]` | 按 classical 处理 |
| 纯域名列表 / Surge DOMAIN-SET | 每行一个域名 | 见下方的点号规则 |
| 纯 IP 段列表 | 每行一个 CIDR | 作为 IP 部分 |
| AdGuard / Adblock 过滤列表 | `\|\|example.com^` 这种写法 | 只取域名拦截条目，与 sing-box 的 adguard 转换语义一致 |
| hosts 文件 | `0.0.0.0 example.com` | 取出域名 |
| mihomo .mrs | 文件头 `MRS` | 见第 4 节 |

**点号规则（实现时必须用测试确认）：**

- Clash 的域名写法：`+.a.com` 表示 a.com 及其所有子域名；`.a.com` 只表示子域名；`*.a.com` 只表示一级子域名，转成正则。
- Surge 的 DOMAIN-SET 写法：`.a.com` 表示 a.com 及其所有子域名。
- 无法判断来自哪种软件时，根据来源提示决定：已知项目、或路径里包含 `surge`/`clash`。都没有时按 Clash 处理。

**Clash classical 类型的转换：**

| Clash | sing-box |
|---|---|
| DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD / DOMAIN-REGEX | domain / domain_suffix / domain_keyword / domain_regex |
| DOMAIN-WILDCARD | domain_regex |
| IP-CIDR / IP-CIDR6 | ip_cidr（IP 部分）。`no-resolve` 忽略，因为 IP 部分本来就排在后面 |
| SRC-IP-CIDR / SRC-PORT / DST-PORT | source_ip_cidr / source_port / port、port_range |
| PROCESS-NAME / PROCESS-PATH | process_name / process_path |
| NETWORK | network |
| GEOSITE,x / GEOIP,x | 展开成 MetaCubeX 对应名字的数据，并入当前来源 |
| IP-ASN,n | MetaCubeX 的 `asn/ASn.srs` |
| AND / OR / NOT | logical 规则（NOT 转成 invert） |
| USER-AGENT、URL-REGEX、IN-PORT、SUB-RULE 等 | 不支持，计数并在详情里列出 |

## 4. mihomo .mrs

常见的 .mrs 来源，都能在同一个项目里找到同名的 sing-box 版本。客户端内置一张对应表，例如：

- MetaCubeX：`meta/geo/geosite/x.mrs` 对应 `sing/geo/geosite/x.srs`，geoip 同理；
- DustinWin：`mihomo-ruleset` 对应 `sing-box-ruleset`；
- Loyalsoldier/geoip：mrs 对应 srs。

遇到不在表里的 .mrs，标为"暂不可用"，并提示用户换一个 sing-box 或 Clash 文本格式的链接。是否自己实现 .mrs 解码，放到以后再定；实现前需要先确认 mihomo 代码的许可证。

## 5. 下载与镜像

- **由谁下载。** 下载由 GUI 进程完成，agent 不联网。
- **已连接时**，下载请求本身就经过 TUN 和分流规则，能正常访问 GitHub。
- **未连接时**，按顺序尝试：GitHub 原始地址 → jsDelivr（仅限 GitHub 仓库文件）→ 用户设置的自定义镜像前缀。
- **镜像前缀。** 订阅里常见形如 `https://某镜像/https://github.com/…` 的地址，先剥掉前缀，按真实来源去下载，不依赖第三方镜像。
- **下载限制。** 单个文件最大 32 MiB，并且必须能正确解析，否则视为失败，保留上一份。
- **首次连接时缺数据**，沿用原来的做法：最多等 25 秒；拿不到数据的分流项暂时跳过，并提示用户。

## 6. 更新

- **每个来源独立记录**：更新时间、ETag、内容哈希。
- **默认更新频率**：每 24 小时检查一次（原来是 72 小时，规则集通常每天更新）。可以在设置里调整。
- **内容变化时**重新编译受影响的分流项。连接中按原有的"刷新后重新应用"设置决定是否立刻应用。
- **同一个文件只下载一次。** 多个方案或多个分流项引用同一个文件时，共用一份。

## 7. 内置规则目录

目录随应用发布。它只包含名字、说明和下载地址，不包含规则数据本身，所以不涉及重新分发规则数据。目录分为三部分：

**1. 精选条目，约 60 到 100 个。** 每个条目包括：

- 标识、中英文名称、图标；
- 搜索用的别名，例如"奈飞""网飞"都指向 Netflix；
- 类别：拦截、AI、流媒体、社交、通讯、开发、游戏、云、金融、国内、海外；
- 推荐来源，以及若干备选来源；
- 默认走向、默认插入优先级。

**2. 名称快照。** 包括 MetaCubeX geosite 和 blackmatrix7 Clash 分类（约 670 个名字），随应用版本更新。MetaCubeX geosite 的快照是 2019 个名字，两种格式（.srs、.mrs）共 4038 个文件，不是最初估计的"约 4000 个名字"。

**3. 收录的项目：**

| 用途 | 项目 | 格式 |
|---|---|---|
| 基础数据（默认） | MetaCubeX/meta-rules-dat（sing 分支） | .srs：geosite、geoip（含服务 IP）、ASN |
| 基础数据（备选） | SagerNet/sing-geosite、sing-geoip；Loyalsoldier/geoip；DustinWin/ruleset_geodata | .srs |
| 按服务分类 | blackmatrix7/ios_rule_script；Loyalsoldier/clash-rules；ACL4SSR | Clash，需要转换 |
| 按服务分类 | SukkaW/Surge（ruleset.skk.moe）；VPSDance/ai-proxy-rules | sing-box JSON |
| 去广告 | anti-AD；217heidai/adblockfilters；秋风 AWAvenue | .srs / JSON |
| 去广告 | hagezi/dns-blocklists | Adblock 格式，需要转换 |
