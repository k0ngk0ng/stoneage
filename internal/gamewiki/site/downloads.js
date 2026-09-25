"use strict";
(async()=>{
  const area=document.getElementById('sactl-download');
  if(!area)return;
  try{
    const configured=document.getElementById('media-config').getAttribute('content');
    const root=new URL(configured.replace(/\/$/,'')+'/');
    if(root.protocol!=='https:'||root.username||root.password)throw new Error('客户端 CDN 暂未配置。');
    const response=await fetch(new URL('downloads/sactl/latest.json',root),{cache:'no-cache'});
    if(!response.ok)throw new Error('客户端安装包尚未发布，请稍后再试。');
    const catalog=await response.json(),version=catalog.version;
    if(!/^v\d+\.\d+\.\d+$/.test(version)||!Array.isArray(catalog.packages))throw new Error('客户端版本信息无效。');
    const folder=`downloads/sactl/${version}/`;
    const script=ext=>{
      const expected=folder+'install-sactl'+ext;
      if(catalog.installers?.[ext]!==expected)throw new Error('安装脚本信息无效。');
      return new URL(expected,root).href;
    };
    const sh=script('.sh'),ps=script('.ps1'),base=root.href.replace(/\/$/,'');
    const quote=s=>"'"+s.replace(/'/g,"'\\''")+"'";
    const psquote=s=>"'"+s.replace(/'/g,"''")+"'";
    const unix=`curl -fsSL ${quote(sh)} | bash -s -- --download ${version} --cdn-base ${quote(base)}`;
    const win=`& ([scriptblock]::Create((Invoke-WebRequest -UseBasicParsing ${psquote(ps)}).Content)) -Download -Version ${psquote(version)} -CdnBase ${psquote(base)} -AddToPath`;
    const rows=[['darwin','macOS','支持 Apple 芯片与 Intel；脚本自动识别架构。',unix],['windows','Windows','支持 x64；Windows ARM64 使用 x64 兼容运行。请在 PowerShell 中执行。',win],['linux','Linux','支持 x86_64 与 ARM64；需要 curl、tar 和 SHA-256 工具。',unix]];
    const nodes=[];
    for(const [os,title,hint,command] of rows){
      const card=document.createElement('article');card.className='client-download-card';
      const heading=document.createElement('h3');heading.textContent=`${title} · ${version}`;
      const note=document.createElement('p');note.textContent=hint;
      const pre=document.createElement('pre'),code=document.createElement('code');code.textContent=command;pre.append(code);
      const copy=document.createElement('button');copy.type='button';copy.textContent='复制安装命令';copy.onclick=async()=>{try{await navigator.clipboard.writeText(command);copy.textContent='已复制';}catch{copy.textContent='请选中下方命令复制';}};
      const links=document.createElement('p');links.className='client-download-links';
      const expectedArch=os==='windows'?['amd64']:['arm64','amd64'];
      for(const arch of expectedArch){
        const pack=catalog.packages.find(p=>p.os===os&&p.arch===arch);
        const expected=folder+`stoneage-sactl-${version}-${os}-${arch}${os==='windows'?'.zip':'.tar.gz'}`;
        if(!pack||pack.path!==expected||!Number.isSafeInteger(pack.bytes)||pack.bytes<=0||!/^[a-f0-9]{64}$/.test(pack.sha256))throw new Error('客户端安装包信息无效。');
        const link=document.createElement('a');link.href=new URL(pack.path,root).href;
        link.textContent=`手动下载 ${arch==='amd64'?(os==='darwin'?'Intel':'x64'):'ARM64'}（${(pack.bytes/1048576).toFixed(1)} MB）`;
        links.append(link);
      }
      card.append(heading,note,copy,pre,links);nodes.push(card);
    }
    const checksum=document.createElement('a');checksum.href=new URL(folder+'SHA256SUMS',root).href;checksum.textContent='查看 SHA-256 校验清单（安装脚本会自动校验）';nodes.push(checksum);
    area.replaceChildren(...nodes);
  }catch(error){area.textContent=error.message||'客户端查询失败，请刷新重试。';}
})();
