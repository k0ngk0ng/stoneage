StoneAge Revival 2.5 - Windows 11 x64
=====================================

用戶端仍是原生 32 位 x86 程式，Windows 11 可直接執行；cnc-ddraw 已包含。
文字底層是 CP936/GBK，請勿開啟「Beta: 使用 Unicode UTF-8」來修復亂碼。
套件不包含開發機的登入密碼、聊天記錄、郵件或其他本機快取。

加入朋友的伺服器：
1. 安裝 Python 3（只在設定地址時需要）。
2. 執行：powershell -ExecutionPolicy Bypass -File .\Configure-Server.ps1 -IPv4 192.168.1.50
3. 執行：powershell -ExecutionPolicy Bypass -File .\Start-StoneAge.ps1
4. 選擇「本機」與「本機一線」。VPN 聯機時請填入 VPN IPv4。

在本機跑網關（另需 Linux 伺服器已監聽 127.0.0.1:19065）：
  powershell -ExecutionPolicy Bypass -File .\Start-StoneAge.ps1 -LocalGateway

停止：
  powershell -ExecutionPolicy Bypass -File .\Stop-StoneAge.ps1

詳細資料位於 docs\Windows-11.md 與 docs\Networking.md。
