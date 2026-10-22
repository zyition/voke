; ===========================================================================
; voke 安装器脚本（Inno Setup 7.1.0）
; ---------------------------------------------------------------------------
; 定位：自用安装器。per-user（不弹 UAC）、单 exe、升级时原地覆盖识别。
; 编译入口：packaging/build-installer.sh（调 Windows 侧 ISCC.exe，产物落 packaging/dist/）。
;
; 本文件里每条指令都对照 Inno Setup 7 官方帮助核实过，出处写在各段注释里；
; 没有对症的指令一律不写（宁可少写，不编造）。
; ===========================================================================

; 版本号：写进 AppVersion / 产物文件名。
;
; Go exe 没有 VERSIONINFO 版本资源，故不从 exe 读版本；发版与本地打包都由
; packaging/build-installer.sh / release.yml 通过 `-dVokeVersion` 传入 tag 版本。
; 直接跑 ISCC 而不传 -d 时才会用下面的 0.0.0 兜底（仅供语法检查）。
#ifndef VokeVersion
  #define VokeVersion "0.0.0"
#endif

; 待打包的 exe。正式构建由脚本 / CI 用 `-dVokeExe` 指向本次 Go 产物；
; 这里的默认值只是让直接跑 ISCC 时有一个明确路径，需先在仓库根目录
; 构建出 voke.exe。
#ifndef VokeExe
  #define VokeExe "..\voke.exe"
#endif

[Setup]
; 固定 AppId：升级/重装时 Setup 靠它认出"是同一个应用"从而原地覆盖，而不是并列装第二份。
; 自用场景用固定字符串即可，不必是 GUID（AppId 只要求同一台机器内唯一、且不含反斜杠）。
AppId=Voke
AppName=Voke
AppVersion={#VokeVersion}
AppPublisher=zyition

; per-user 安装到 {localappdata}\Programs\Voke（= %LOCALAPPDATA%\Programs\Voke），
; 配合 PrivilegesRequired=lowest：全程不申请管理员权限，{localappdata} 天然可写。
DefaultDirName={localappdata}\Programs\Voke
PrivilegesRequired=lowest

; voke.exe 是 x86_64 二进制，禁止装到非 x64 兼容系统。
; 注意：Inno 7 的 x64 版编辑器**默认产出 32-bit Setup**，其 ArchitecturesAllowed
; 默认值是 x86compatible，所以这里必须显式收紧为 x64compatible。
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible

; 自用、无分组需求：隐藏"选择开始菜单文件夹"页。开始菜单入口见 [Icons]。
DisableProgramGroupPage=yes

; 「应用和功能」里显示的卸载项名称与图标。
UninstallDisplayName=Voke
UninstallDisplayIcon={app}\voke.exe

; 安装器自身 exe 的图标，复用应用图标。
SetupIconFile=..\assets\voke.ico

; 产物目录与文件名：相对 SourceDir（默认 = 本 .iss 所在目录），即 packaging/dist/。
OutputDir=dist
OutputBaseFilename=voke-setup-{#VokeVersion}

; 压缩：Inno 7 的默认就是 lzma2/max，显式写出便于日后调整。
Compression=lzma2/max
SolidCompression=yes

; 现代向导外观（Inno 6+）。
WizardStyle=modern

; ----- 安装前结束正在运行的 voke -----
; **不用 Restart Manager**（CloseApplications=no），改用 [Code] 里的 taskkill：
;
;   * voke 是常驻托盘应用，关闭窗口 ≠ 退出进程（面板/设置窗的 WM_CLOSE 只隐藏或
;     销毁窗口，进程继续驻留托盘）。
;   * 默认 yes：Restart Manager（下称 RM）的非强制关闭等进程自己退出，托盘进程永远
;     不满足，RmShutdown 返回 351（ERROR_FAIL_SHUTDOWN），安装器弹"无法关闭应用
;     程序"，用户只能手动退出托盘再装。
;   * force 也不行：官方文档明确 RmForceShutdown 会先发关闭请求，**不响应的应用
;     要等 30 秒后才被强制关闭**。实测 v0.2.2 覆盖安装每次都要等满约 30 秒。
;     （Rust 版很快是因为 gpui 在窗口关闭后退出进程；Go 版按托盘语义保持常驻。）
;   * 故直接 taskkill /F，与 [UninstallRun] 同款：瞬时结束、无 30 秒等待。voke 无
;     未保存数据（设置即时落盘），强制无风险。
;
; [Code] 的 CurStepChanged(ssInstall) 在 [Files] 之前执行；ewWaitUntilTerminated 等
; taskkill 真正结束进程后才继续，保证覆盖 voke.exe 时文件已不被占用。taskkill 在
; 进程未运行时返回非零，属正常（没运行就不需要关），忽略。
;
; 与 AppMutex 的区别：AppMutex 需要应用 CreateMutex 一个约定名，Setup 检测到只弹
; "请手动关闭再点确定"，不代劳。Go 版单实例走 mygo 的 AF_UNIX socket，没有可被
; Setup 检测的命名互斥体；即便有，也解决不了"谁去关进程"的问题。故不用 AppMutex。
CloseApplications=no

[Languages]
; 官方内置简体中文：compiler: 前缀表示到"Inno 安装目录"里找该 .isl
; （已核实安装目录下存在 Languages\ChineseSimplified.isl）。只此一种语言，
; 不会弹语言选择框。
Name: "chinesesimplified"; MessagesFile: "compiler:Languages\ChineseSimplified.isl"

[Files]
; voke.exe → {app}（= {localappdata}\Programs\Voke）。
; ignoreversion：Go exe 没有 VERSIONINFO 版本资源，别让 Setup 做版本号比较。
Source: "{#VokeExe}"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
; 开始菜单放一个入口，{autoprograms} = 当前用户开始菜单的"程序"目录。
; 刻意**不放桌面图标**：常驻托盘的启动器，桌面图标是噪音。
Name: "{autoprograms}\Voke"; Filename: "{app}\voke.exe"

[Registry]
; 自启的 HKCU\...\Run\voke 值由**应用自己在设置页**写/删（见
; internal/platform/autostart_windows.go）；
; 安装器不创建它。但卸载时必须清掉，否则会在 Run 里留一条指向已删 exe 的死项。
;
; 语义（官方 [Registry] 帮助核实）：
;   ValueType: none          → Setup「创建 key 但不创建 value」（Run 键本就存在，等于不写值）；
;   Flags: uninsdeletevalue  → 卸载时删除 ValueName 指定的那个 value。
; 两者组合 = "安装不建、卸载删"，正合所需。
; （帮助里另有一条 NOTE：ValueType none 与旧 flag 的组合**已不再**能当"key 为空则删"
;   的用法——那是另一回事，不是我们要的语义。）
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: none; ValueName: "voke"; Flags: uninsdeletevalue

[Run]
; 安装完成后，在向导末页提供一个「运行 Voke」勾选框，勾选则装完立即启动。
; 官方 [Run] 帮助核实：
;   * postinstall：该条目出现在向导**完成页**（复选框形式），而非安装过程中；
;   * skipifsilent：静默安装（/SILENT、/VERYSILENT）时不启动——静默场景不该弹进程；
;   * nowait：启动后不等待其退出，向导立即结束（voke 是常驻托盘，等它=向导永不结束）。
;   * 复选框默认**勾选是 postinstall 的官方默认语义**（不勾不启动，用户可取消）。
; 说明：
;   * Description 用官方自带消息 {cm:LaunchProgram,Voke}；已核实
;     compiler:Languages\ChineseSimplified.isl 的 [CustomMessages] 里有
;     「LaunchProgram=运行 %1」（该 .isl 第 412 行），中文下显示「运行 Voke」。
;     故无需在 [CustomMessages] 里自定义。
;   * {app}\voke.exe 启动后走单实例 + 托盘（mygo 的 per-user socket 锁），
;     不弹主窗口是正常语义，不是没启动。
;   * 与 [UninstallRun] 无冲突：本段只在【安装】时执行，[UninstallRun] 只在【卸载】时
;     执行，二者时机互斥、互不影响。
;   * 与上面 RestartApplications=no 不矛盾：后者管的是"Setup 为替换文件而关掉的进程
;     是否被自动拉起"（这里保持关闭，不越俎代庖）；本段的启动是用户在完成页显式勾选的
;     一次性动作，两码事。
; 「立即运行」经 explorer.exe 代启动：运行中的 explorer 会把请求转交给登录时
; 创建的干净 shell 实例，由它启动 voke——父进程/令牌/job/环境与快捷方式启动
; 同款。安装器直接拉起的子进程会继承安装器进程上下文，其后代（WT profile →
; pwsh → scoop shim）会 junction 遍历失败（mise 起不来），故绕开直启。
; 转义规则：Inno 的 """ 产出字面引号，故 Parameters 实际传给 explorer 的是
; 带引号的 "{app}\voke.exe"（explorer 命令行需带引号路径才认）。
Filename: "{win}\explorer.exe"; Parameters: """{app}\voke.exe"""; Description: "{cm:LaunchProgram,Voke}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; 卸载的**第一步**执行（官方 [Run] & [UninstallRun] 帮助：「the [UninstallRun] section
; ... specifies any number of programs to execute as the first step of uninstallation」）。
;
; 为什么卸载侧要单独写：CloseApplications 只作用于【安装】（官方帮助全篇只讲 Setup），
; 卸载器不会自动关闭占用文件的进程——不先结束 voke，卸载器就删不掉被占用的 voke.exe。
; 所以这里用 taskkill 显式结束它。
;   * {sys} = 系统目录；32-bit Setup 下是 SysWOW64（其中同样有 taskkill.exe）。
;   * /F 强制结束；voke 无未保存数据，强制无风险。
;   * RunOnceId：同一应用重复安装时，保证该条目在卸载日志里只保留一条、只跑一次。
Filename: "{sys}\taskkill.exe"; Parameters: "/IM voke.exe /F"; RunOnceId: "KillVoke"

; 说明：卸载**保留** %APPDATA%\voke（用户配置/日志），故本脚本不写任何删除它的语句。

[Code]
// 安装开始前（[Files] 之前）强制结束托盘里的 voke。
// 为什么不用 CloseApplications：见 [Setup] 处的长注释（RM 对托盘应用要等 30 秒）。
procedure CurStepChanged(CurStep: TSetupStep);
var
  ResultCode: Integer;
begin
  if CurStep = ssInstall then
  begin
    if Exec(ExpandConstant('{sys}\taskkill.exe'), '/IM voke.exe /F', '',
      SW_HIDE, ewWaitUntilTerminated, ResultCode) then
      Log(Format('taskkill voke.exe 完成，退出码 %d', [ResultCode]))
    else
      Log('无法启动 taskkill（若 voke.exe 正在运行，覆盖文件时会被系统阻止）');
  end;
end;
