# 分流与节点选择设计

EggBox 分流（原"规则"）和节点选择的设计，按主题分成下面几篇。每篇只讲一个主题，各篇不重叠，合起来是完整的设计。每篇描述的都是当前的做法；实现中改过的地方，在原处写明了最初的设计和为什么改。

| 文档 | 讲什么 | 不讲什么（去哪里看） |
|---|---|---|
| [overview.md](overview.md) | 为什么要改（两轮）、目标与原则、非目标、概念、约束、已定决策 | 各项功能的具体做法（下面各篇） |
| [experience.md](experience.md) | 用户看到和操作的部分：首次使用、分流页、添加分流项、我的例外、解析方式、测试网址、连接列表 / 首页 / 托盘、走向与节点选择、按环境切换、状态与提示、暂不做的界面功能 | 方案的行为（plans）、配置怎样生成（config）、节点身份（nodes） |
| [plans.md](plans.md) | 方案的种类、只能选一个的原因、跟随更新的方案和用户叠加层、刷新与切换时保留什么、来源订阅停用 / 删除、内置推荐方案的内容 | 订阅里的分组和规则怎样变成方案（subscription-plans）、方案栏界面（experience） |
| [subscription-plans.md](subscription-plans.md) | 导入订阅时取什么、规则怎样变成分流项、分组怎样变成走向（含广告分组）、验收用的实例 | 导入后的更新和切换（plans）、规则集的下载和格式（rule-sources） |
| [rule-sources.md](rule-sources.md) | 规则数据：sing-box 能用什么、统一处理流程、支持的格式和转换（含点号规则）、.mrs、下载与镜像、更新、内置规则目录 | 添加分流项的界面（experience）、编译结果怎样写进配置和 agent 上限（config） |
| [nodes.md](nodes.md) | 节点身份、编辑导入的节点（原始分享链接） | 节点选择的界面（experience）、节点组怎样生成（config）、旧节点哈希的迁移（data-and-migration） |
| [config.md](config.md) | 生成 sing-box 配置：出口结构（含 `preferred`）、规则顺序、局域网直连与用户认领的私有段、DNS（按规则指定服务器、fake-ip、resolve 的跳过条件）、agent 侧的调整、系统路由的验证结论 | 用户怎样设置这些（experience）、规则数据的编译（rule-sources）、"用户写的"标记怎样保存（data-and-migration） |
| [data-and-migration.md](data-and-migration.md) | 数据格式的版本规则、从旧版本的迁移、分流状态格式 1 → 2 的"用户写的"标记 | 标记在配置里的作用（config）、节点原文的保存（nodes）、来源的更新记录（rule-sources） |
| [delivery.md](delivery.md) | 分阶段交付的内容和验收（第一轮四个阶段、第二轮） | 编辑导入节点的验收（nodes） |

进程架构和安全边界见 `docs/design.md`，本组文档不改变它们。
