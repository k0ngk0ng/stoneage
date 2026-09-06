# 阿里云 CDN 自动证书

适用于 `ichenj.com` 使用阿里云 DNS、`cdn.ichenj.com` 使用阿里云 CDN 的部署。服务器需要 Python 3、Certbot 和可用的 Certbot 账户；不需要 aliyun CLI。证书由 Let's Encrypt 签发，直接部署到 CDN，游戏容器不用挂载证书。

1. 将 `aliyun.json.example` 复制为 `aliyun.json`，按实际域名及路径调整。
2. 专用 RAM 用户授权 `alidns:DescribeDomainRecords`、`alidns:AddDomainRecord`、`alidns:DeleteDomainRecord`、`cdn:DescribeCdnDomainDetail`、`cdn:SetCdnDomainSSLCertificate`。如策略使用 `Resource: "*"`，权限覆盖账号内域名；可在 RAM 控制台按支持的资源范围继续收窄。
3. AK/SK 分别写入 `access-key-id`、`access-key-secret`，目录权限 700、密钥权限 600，禁止提交到 Git。

首次申请（以下为本机部署路径）：

```bash
python3 /opt/stoneage/bin/aliyun-certificate.py check
certbot certonly --manual --preferred-challenges dns \
  --manual-auth-hook 'python3 /opt/stoneage/bin/aliyun-certificate.py auth' \
  --manual-cleanup-hook 'python3 /opt/stoneage/bin/aliyun-certificate.py cleanup' \
  --deploy-hook 'python3 /opt/stoneage/bin/aliyun-certificate.py deploy' \
  --cert-name cdn.ichenj.com -d cdn.ichenj.com --non-interactive
certbot renew --cert-name cdn.ichenj.com --dry-run
```

Certbot 保存验证及部署钩子，系统的 `certbot.timer` 负责续期。DNS 钩子只添加本次 `_acme-challenge` TXT 记录，验证后删除，不修改网站或 CDN 的解析。

可安装本目录的 `stoneage-cdn-certificate.service`、`.timer` 到 `/etc/systemd/system/`，执行 `systemctl daemon-reload && systemctl enable --now stoneage-cdn-certificate.timer`。该任务每天检查 CDN 实际证书，已一致则跳过，否则重新部署当前有效证书，用于重试签发成功但 CDN 上传暂时失败的情况。

```bash
systemctl list-timers certbot.timer stoneage-cdn-certificate.timer
journalctl -u stoneage-cdn-certificate.service -n 30
curl -I https://cdn.ichenj.com/stoneage/_client-version.json
```

私钥由 Certbot 保存在 `/etc/letsencrypt/live/cdn.ichenj.com/`。备份时同时包含 `/etc/letsencrypt/` 与 `config/certificates/`，并保护备份权限。更新 AK/SK 只需修改密钥文件。本机已按授权复用 `config/secrets/oss-access-key-id` 和 `oss-access-key-secret`，实际路径以 `aliyun.json` 为准，无需再维护一份密钥。
