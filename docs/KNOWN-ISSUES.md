# voke 已知限制（KNOWN-ISSUES）

> 已确认、当前版本存在的限制与行为。区别于 [`BACKLOG.md`](BACKLOG.md)（未来工作）。
> 正文简体中文。

---

## 1. 上游平台层拖放目标泄漏（设置窗）

- **现象**：以「放行 WM_CLOSE」的方式关闭设置窗会滞留整套 GPU 资源（每关一次约 +26.5MB）。
- **原因**：上游 `gpui-pre-windows` 的 `register_drag_drop`（window.rs:571/1511）把窗口内部
  引用存进 OLE `IDropTarget`，而 `RevokeDragDrop` 放在窗口 `Drop` 的延迟异步任务里
  （window.rs:610-631），执行时 HWND 已销毁必然失败 → 资源不被释放。
- **规避办法**：本仓把设置窗改为「**关闭 = 程序化销毁**」（Zed 主窗同款姿势）：
  `on_window_should_close` 里返回 `false` 吞掉 WM_CLOSE，改用
  `window.remove_window()` 让 gpui 自己走「trail 同步摘除 → Drop 任务在 HWND
  存活期内 `RevokeDragDrop` + `DestroyWindow`」，26.5MB 全回收（详见
  [`HISTORY.md`](HISTORY.md) 第 6 节与 `settings.rs` 里注册处的长注释）。
- **是否计划修**：**否（不修上游）**。用户明确拒绝 vendor / patch 上游。已知会向
  上游提交 issue 文稿（最小复现 + 根因 + 建议修法：把 `RevokeDragDrop` 提前到 `DestroyWindow`
  前同步执行）。

## 2. 设置页 Group / Page 标题层级无法自定义

- **现象**：「插件」页里各插件组的视觉层级（加粗 / 缩进 / 色带）做不出来。
- **原因**：`gpui-component` 的 `SettingGroup::title` 与 `SettingPage::title` 只收
  `impl Into<SharedString>`（纯文本），标题容器与排版写死在框架内部；`title_suffix`
  只能加尾巴、不够用。
- **规避办法**：接受当前纯文本标题；或放弃 setting 框架改自渲染（未采用）。
- **是否计划修**：**未决策**，等真机观感反馈后再定。

## 3. 默认热键 `alt+space` 只对新配置生效

- **现象**：全新安装（无配置文件 / TOML 未写 `hotkey`）才用默认 `alt+space`；老配置里已落盘的
  旧值（如 `ctrl+alt+space`）不会被自动改写。
- **原因**：`from_toml` 原样读取旧值，`normalize` 仅在热键非法时才回落默认——刻意不做迁移。
- **规避办法**：老用户手动在设置页改键，或删掉 `config.toml` 里的 `hotkey` 行。
- **是否计划修**：**不修，这是预期行为，非 bug**。记录在此以免日后被当成 bug 重开。

---

## 4. 其它已知限制

- **`cargo check -p voke-app` 在 Linux native 失败（环境性）**：gpui 的 Linux 后端依赖
  `fontconfig.pc`，缺此包时 build.rs 直接失败。`voke-app` 本就只面向 Windows，gnu/msvc
  交叉目标验收不受影响，**属预期，不要修**。
- **`Win+Shift+S` 截图时窗口偶发消失（低优先级）**：应用在前台按截图键有时触发失焦回调走到
  `SW_HIDE`，与截图覆盖层下不稳定的前台/激活语义有关。待排查（可能加失焦延迟判定）。
- **汉字 query 排序偏低**：`微信` 等含汉字查询的匹配得分低于其拼音，不影响命中性，仅排序手感
  偏后（见 [`BACKLOG.md`](BACKLOG.md)）。
