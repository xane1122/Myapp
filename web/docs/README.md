# 文档索引

本目录保存项目的产品、领域、架构、数据模型和 Context 设计文档。

## 推荐阅读顺序

1. [产品需求文档](PRD.md)：先了解产品目标、功能范围、非功能需求和验收标准。
2. [软件设计说明书](SDD.md)：作为实现、测试和验收总纲，串联全部设计文档。
3. [领域驱动设计文档](DDD.md)：理解核心领域、子域、限界上下文、聚合和不变量。
4. [架构文档](ARCHITECTURE.md)：理解 Go 服务、静态前端、SQLite、本地文件和 HTTP 路由的整体结构。
5. [数据 E-R 模型](ER-MODEL.md)：查看 SQLite 表、字段、关系和数据生命周期。
6. [概要设计](HIGH-LEVEL-DESIGN.md)：查看主要用例、模块职责、安全设计和降级策略。
7. [Context 详细设计](CONTEXT-DETAILED-DESIGN.md)：查看 JSON Context Builder 的输入、输出、路由、过滤、注入和调试设计。
8. [iPhone 当前屏幕工具接入](SCREEN-PEEK-SETUP.md)：配置邮件触发、快捷指令截图上传和端到端验收。

## 现有 Context 专题文档

- [当前 Context 架构](context-current-architecture.md)：记录 legacy 混合提示词路径及其风险。
- [JSON Context 结构说明](context-json-schema.md)：记录 JSON Context 的字段边界和校验规则。
- [下一版本 PR：情感连续性长期记忆召回](NEXT-PR-memory-continuity.md)：记录下一版本核心功能规划。
- [下一版本 Codex 开发启动简报](NEXT-CODEX-BRIEF-memory-continuity.md)：用于新开对话时指导 Codex 直接进入实现。

## 维护约定

- 变更数据库表结构时，同步更新 [数据 E-R 模型](ER-MODEL.md)。
- 变更 `/api/*` 路由、工具定义或上下文构建链路时，同步更新 [架构文档](ARCHITECTURE.md) 和 [Context 详细设计](CONTEXT-DETAILED-DESIGN.md)。
- 变更记忆、文档、人设、会话隔离规则时，同步更新 [领域驱动设计文档](DDD.md)。
- 变更整体设计目标、验收边界或演进计划时，同步更新 [软件设计说明书](SDD.md)。
- 涉及用户可感知能力变化时，同步更新 [产品需求文档](PRD.md) 和 [用户使用手册](用户使用手册.md)。
