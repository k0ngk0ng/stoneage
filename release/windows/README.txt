StoneAge Revival 2.5 - Windows 11 x64
=====================================

用戶端仍是原生 32 位 x86 程式，Windows 11 可直接執行；cnc-ddraw 已包含。
文字底層是 CP936/GBK，請勿開啟「Beta: 使用 Unicode UTF-8」來修復亂碼。
套件不包含開發機的登入密碼、聊天記錄、郵件或其他本機快取。

加入朋友的伺服器：
1. 安裝 Python 3（只在設定地址時需要）。
2. 執行：powershell -ExecutionPolicy Bypass -File .\Start-StoneAge.ps1 -Server 192.168.1.50
3. 啟動器會由唯讀的 sa_2903.exe 產生 sa_2903-local.exe；原版 EXE 不會被修改。
4. 選擇「本機」與「本機一線」。VPN 聯機時請填入 VPN IPv4。

要顯示多個伺服器/線路時，編輯套件內的 client-servers.toml.example，另存為
client-servers.toml，再由啟動器讀取：
  powershell -ExecutionPolicy Bypass -File .\Start-StoneAge.ps1 -ServersFile .\client-servers.toml

也可以直接從 HTTPS 接口取得：
  powershell -ExecutionPolicy Bypass -File .\Start-StoneAge.ps1 -ServersUrl https://example.com/servers.toml

配置格式同樣適用於 macOS/Linux 的客戶端生成腳本；舊客戶端會在生成時嵌入列表。

在本機跑網關（另需 Linux 伺服器已監聽 127.0.0.1:19065）：
  powershell -ExecutionPolicy Bypass -File .\Start-StoneAge.ps1 -LocalGateway

停止：
  powershell -ExecutionPolicy Bypass -File .\Stop-StoneAge.ps1

詳細資料位於 docs\Windows-11.md 與 docs\Networking.md。
