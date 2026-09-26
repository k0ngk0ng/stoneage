#!/usr/bin/env python3
"""Build lazy encyclopedia material references and reusable, offline creation ZIPs.
Reads existing PNGs/manifests only; does not extract, upload or modify game assets.
"""
import argparse
import collections
import csv
import io
import zipfile
import urllib.request
import asset_cooker
import gzip
import hashlib
import json
import re
import struct
from pathlib import Path


def tone_file(n):
    # Matches runtimeassets/index.html::soundFileForTone; tested against that function.
    if 1 <= n <= 11: return f'sap_{n:02}.wav'
    if n in (12, 13, 14, 130): return {12:'sap_14.wav',13:'sap_12.wav',14:'sap_13.wav',130:'sap_14.wav'}[n]
    for lo, hi, prefix, offset in [(51,82,'sae',50),(100,123,'sam',99),(151,168,'sak',150),(201,221,'sas',200),(250,254,'sad',249),(300,326,'saam',299)]:
        if lo <= n <= hi:
            if n == 214: return None
            if n in (159,168): return 'sak_09a.wav' if n == 159 else 'sak_09b.wav'
            return f'{prefix}_{n-offset:02}.wav'
    return None


def encoded(value):
    return json.dumps(value,ensure_ascii=False,separators=(',',':'),sort_keys=True).encode()


def write(path, raw):
    path.parent.mkdir(parents=True,exist_ok=True)
    path.write_bytes(gzip.compress(raw,mtime=0))


def ride_table(root):
    source=(root/'char/char_base.c').read_text(encoding='gb18030',errors='replace').split('ridePetTable[296] =',1)[1].split('};',1)[0]
    version=(root/'include/version.h').read_text(encoding='gb18030',errors='replace')
    gm=bool(re.search(r'^\s*#define\s+_GM_METAMO_RIDE\b',version,re.M))
    rows=[];active=True
    for line in source.splitlines():
        line=line.split('//',1)[0].strip()
        if line.startswith('#ifdef _GM_METAMO_RIDE'):active=gm
        elif line.startswith('#ifndef _GM_METAMO_RIDE'):active=not gm
        elif line.startswith('#endif'):active=True
        elif active:
            m=re.fullmatch(r'\{\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\},?',line)
            if m:rows.append(tuple(map(int,m.groups())))
    assert len(rows)==290, 'Native ride initializers changed; verify active definitions'
    # The declared 296-element C array has six zero-filled trailing entries.
    result=collections.defaultdict(dict)
    for ride,person,pet,template in rows:result[str(pet)].setdefault(str(ride),[]).append(person)
    # modern/patches/0011-white-tiger-ride-mapping.patch, separately verified source group.
    for i in range(12):result['100872'][str(104025+i)]=list(range(100000+i*20,100020+i*20,5))+list(range(100700+i*10,100710+i*10,5))
    return result


README='''# 素材制作参考包

解压后双击 preview.html 查看图片、动画和试听音频。素材引用全部为包内相对路径，可以离线使用。请勿直接覆盖当前游戏同编号资源。

## 文件
- assets/、audio/、wiki/maps/：实际引用的原始图片和音频（保留原文件名）。
- sprites.json：动画图号、每个方向/动作、帧顺序、x/y/xoffset/yoffset 与 sound 事件。
- bitmap-index.json：已核对的逻辑图号与图片、尺寸、绘制偏移、优先级等索引。
- reference.json：图片尺寸、动画列表、骑乘人物映射、缺项说明。
- entries.json：使用这些素材的百科条目与现有配置参数，供制作参考；同素材的条目共用一个包。
- new-asset.template.json：制作新素材时填写的新编号与资源关联草稿，不是可直接导入的游戏配置。
- frames.csv：所有动画的方向、动作、帧号、tick、事件和绘制偏移。
- 若是地图包，还包含 map/ 下原始布局、可编辑分层 JSON、地图图块索引和标记，及 auto.dat/调色板。
- SHA256SUMS：文件哈希清单，用于检查包内文件是否完整。

## 新素材必须保留的规格
动画帧使用透明 PNG；贴图与图鉴按原始尺寸与 alpha 处理，图鉴允许原画背景。原始帧通常按内容裁切，不可把所有小图逐帧居中，否则角色会抖动。绘制坐标 = 固定角色锚点 + (x+xoffset, y+yoffset)。如改为统一画布，必须同步修改偏移并检查每个动作的边缘。

frame_ms 字段保存原生 tick，不是毫秒。战斗/非移动场景动画每 tick 为 1000/60 ms；地图实际移动采用 8 ms/tick。样例帧数由 sprites.json 逐动作给出，不要求不同宠物一律相同。方向 0–7 为下、左下、左、左上、上、右上、右、右下。攻击、受伤、死亡、防御通常不循环；预览的循环仅方便检查。

sound=0 表示无事件；1–9999 是真实声音编号；10000–10099 是命中/接触事件；10100 以上是连击事件。不要删除后两类事件，也不要把它们做成 WAV。事件只决定呈现时机，不自动增加攻击次数或技能。新音效需要分配并接入声音编号映射，不是上传 WAV 即生效。

骑乘是人物和宠物组合动画，不是放大本体。reference.json 保留骑乘图号对应的人物图号；是否能实际骑乘仍取决于服务端人物族型、宠物、等级、忠诚和骑乘资格规则。

## 地图制作
服务端布局为 LS2MAP：前 6 字节标识，40/42 字节为大端宽/高；44 字节起地面、物体及后续层均为大端 uint16，每层 width×height。map/layout.json 保留头部十六进制、各层与尾部字节，便于编辑后按原格式还原。若包含客户端 DAT，它采用小端宽高和小端 uint16；这是客户端参考，不可当作 LS2MAP 覆盖服务端。

图块编号必须通过 bitmap-index.json 的逻辑图号定位；不能用同号物理人物帧替代。索引保留 xoffset/yoffset/宽高/命中/优先级等。参考客户端等距投影 tile_step=[32,24]，定位逻辑不能仅靠拼接 PNG 猜测。低编号单元可能是控制/音乐标记，不能一概当图块。音乐来自实际地面/物体中的 40–53 标记；无标记地图可能沿用上一张地图的音乐，不随意补配背景音乐。

auto.dat 与原版调色板随地图包保存；地图中有专用天空背景时也附上。包内 PNG 是现有导出的图块，不包含并不具备的分层绘画源文件或完整原版 BIN。重新编排地图后，需要重新构建服务端布局、客户端对应数据/索引和百科全图，并检查碰撞、出口、NPC 放置、图块层次、调色板与音效。

## 制作与接入
先确定统一角色/物品/地图设计，再按每动作每方向制作图像；保留原始参考，不直接覆盖。装备图标、图鉴、动画、宠物模板、地图分别需要未占用的新编号。属性和技能参数参考 entries.json；创建全新技能/交互仍可能需要代码支持。

交付前检查全部帧、定位、透明度、播放节奏、音效和命中帧；地图检查缺失图块和所有布局层。reference.json 的 missing 列出尚未具备的内容，缺项不能用猜测资源补齐。下载包包含已收录的全部关联素材；有缺项时不是完整可导入包。新资源还需接入固定 CDN 路径、缓存与客户端清单，游戏能力同时核对 Web/sactl。
'''


def build(args):
    from PIL import Image
    assets,snapshot,out=args.assets,args.snapshot,args.output
    out.mkdir(parents=True,exist_ok=True)
    manifest=json.loads((assets/'manifest.json').read_text())
    sprites=json.loads((assets/'sprites.json').read_text())['sprites']
    catalog=json.loads(gzip.decompress((snapshot/'catalog.json.gz').read_bytes()))
    entries={}
    for p in sorted((snapshot/catalog['revision']).glob('??.json.gz')):entries.update(json.loads(gzip.decompress(p.read_bytes())))
    fields=lambda e:{f['label']:f['value'] for f in e.get('fields') or []}
    rides=ride_table(args.data.parent)
    bitmaps=manifest['bitmaps'];aliases=manifest.get('bitmap_aliases',{})
    def bitmap(n):return bitmaps.get(str(aliases.get(str(n),str(n)))) or bitmaps.get(str(n))
    names=collections.defaultdict(set)
    for e in entries.values():
        if e['kind']=='pet':names[e['name']].add(fields(e).get('图号'))
    photos={}
    for album in manifest.get('album',[]):
        graphic=str(album.get('spriteGraphic',''))
        if not graphic and len(names[album['name']])==1:graphic=next(iter(names[album['name']]))
        file=manifest.get('album_graphics',{}).get(str(album['graphic']))
        if graphic and file:photos[graphic]='assets/'+file
    sizes={};source_files={};sprite_data={};groups={};key_refs={};missing_source=set()
    map_roots=[args.maps,Path('build/wiki-media/maps')]
    def locate(path):
        if path in source_files:return source_files[path]
        if re.fullmatch(r'assets/bitmaps/(?:white-tiger-8\.5/)?bitmap_\d+\.png',path):p=assets/path.removeprefix('assets/')
        elif re.fullmatch(r'audio/(?:bgm|se)/[a-z0-9_]+\.wav',path):p=args.client/'data'/path.removeprefix('audio/')
        elif re.fullmatch(r'wiki/maps/(?:thumb-)?\d+-[a-f0-9]+\.webp',path):
            p=next((r/Path(path).name for r in map_roots if (r/Path(path).name).is_file()),out/'source-cache'/Path(path).name)
            if not p.exists() and args.cdn:
                p.parent.mkdir(parents=True,exist_ok=True)
                try:
                    with urllib.request.urlopen(args.cdn.rstrip('/')+'/'+path,timeout=30) as r:p.write_bytes(r.read())
                except Exception:missing_source.add(path)
        else:raise ValueError('Unexpected media path '+path)
        if not p.is_file():missing_source.add(path);return None
        source_files[path]=p;return p
    def image(path,caption):
        p=locate(path)
        if not p:return None
        if path not in sizes:
            with Image.open(p) as im:sizes[path]=im.size
        w,h=sizes[path];return {'path':path,'caption':caption,'width':w,'height':h}
    def animation(graphic):
        if graphic in sprite_data:return sprite_data[graphic]
        if graphic not in sprites:return None
        actions=sprites[graphic]['actions'];frames=[f for a in actions for f in a['frames']]
        file_set=sorted({f['file'] for f in frames})
        missing=[]
        for file in file_set:
            if not image('assets/'+file,file):missing.append(file)
        if missing:raise ValueError('Animation frame missing: '+missing[0])
        sounds=[]
        for tone in sorted({f.get('sound',0) for f in frames if 0<f.get('sound',0)<10000}):
            name=tone_file(tone);path='audio/se/'+name if name else ''
            sounds.append({'tone':tone,'path':path if path and locate(path) else ''})
        bounds=[min(f['x']+f['xoffset'] for f in frames),min(f['y']+f['yoffset'] for f in frames),max(f['x']+f['xoffset']+sizes['assets/'+f['file']][0] for f in frames),max(f['y']+f['yoffset']+sizes['assets/'+f['file']][1] for f in frames)]
        value={'graphic':int(graphic),'actions':actions,'bounds':bounds,'sounds':sounds,'unique_frames':len(file_set),'frame_count':len(frames)}
        sprite_data[graphic]=value;return value
    bgm={40:'sabgm_t0.wav',41:'sabgm_t1.wav',42:'sabgm_d0.wav',43:'sabgm_d1.wav',44:'sabgm_d2.wav',45:'sabgm_f0.wav',46:'sabgm_f1.wav',47:'sabgm_f1.wav',48:'sabgm_t1.wav',49:'sabgm_t0.wav',50:'sabgm_t0.wav',51:'sabgm_t1.wav',52:'sabgm_t0.wav',53:'sabgm_t1.wav'}
    # 49=slot21(t0), 50=slot17(t0), 51=slot18(t1), 52=slot19(t0), 53=slot20(t1).
    for key,e in sorted(entries.items()):
        if args.keys and key not in args.keys:continue
        ref={'images':[],'variants':[],'sounds':[],'missing':[]};files=set();extras={};indices={}
        for m in e.get('images') or []:
            im=image(m['path'],m['caption'])
            if im:ref['images'].append(im);files.add(m['path'])
            else:ref['missing'].append('图片 '+m['path'])
        graphic=fields(e).get('图号','')
        if not graphic and e['kind']=='enemy':
            linked=next((entries.get(l['key']) for l in e.get('links') or [] if l['key'].startswith('pet:')),None)
            if linked:graphic=fields(linked).get('图号','')
        info=bitmap(graphic)
        if info:indices[graphic]=info
        variants=[(graphic,'本体' if e['kind']=='pet' else e['name'],[])] if graphic in sprites else []
        for ride,persons in rides.get(graphic,{}).items():
            if ride in sprites:variants.append((ride,'骑乘造型 '+ride,persons))
            else:ref['missing'].append('骑乘动画 '+ride)
        if e['kind']=='pet' and graphic not in sprites:ref['missing'].append('本体动画 '+graphic)
        for g,name,persons in variants:
            a=animation(g);ref['variants'].append({'graphic':int(g),'name':name+' · '+g if not persons else name,'file':'sprite-'+g+'.json','characters':persons})
            files.update('assets/'+f['file'] for action in a['actions'] for f in action['frames'])
            for s in a['sounds']:
                if s['path']:files.add(s['path'])
                else:ref['missing'].append('音效 '+str(s['tone']))
        if e['kind']=='pet':
            photo=photos.get(graphic)
            if photo:
                im=image(photo,'图鉴照片')
                if im:ref['images'].append(im);files.add(photo)
                else:ref['missing'].append('图鉴照片')
            else:ref['missing'].append('未核实图鉴映射')
        if e['kind']=='map':
            if e.get('map'):
                im=image(e['map']['path'],'地图全图')
                if im:ref['images'].insert(0,im);files.add(im['path'])
                extras['map/markers.json']=encoded(e['map'])
            else:ref['missing'].append('地图全图')
            source=args.data/e['sources'][0];raw=source.read_bytes()
            assert raw[:6]==b'LS2MAP'
            w,h=struct.unpack_from('>HH',raw,40);count=w*h
            n=(len(raw)-44)//(count*2);layers=[list(struct.unpack_from('>'+str(count)+'H',raw,44+i*count*2)) for i in range(n)]
            assert n>=2
            extras['map/server.ls2map']=raw
            extras['map/layout.json']=encoded({'width':w,'height':h,'header_hex':raw[:44].hex(),'layers':layers,'layer_names':['ground','objects','flags'],'trailing_hex':raw[44+n*count*2:].hex()})
            tile_numbers=set(layers[0])|set(layers[1])
            client= args.client/'map'/(key.split(':')[1]+'.DAT')
            if client.is_file():
                cw,ch,cl=asset_cooker.read_map(client)
                extras['map/client.DAT']=client.read_bytes();tile_numbers.update(cl[0])
                if len(cl)>1:tile_numbers.update(cl[1])
            for logical in sorted(tile_numbers):
                if logical<100:continue
                info=bitmap(logical)
                if info and info.get('bmp_number')==logical and locate('assets/'+info['file']):
                    indices[str(logical)]=info;files.add('assets/'+info['file'])
                else:ref['missing'].append('图块 '+str(logical))
            for tone in sorted(set(layers[0])|set(layers[1])):
                if tone in bgm:
                    path='audio/bgm/'+bgm[tone]
                    if locate(path):files.add(path);ref['sounds'].append({'tone':tone,'path':path,'name':'地图音乐标记 '+str(tone)})
                    else:ref['missing'].append('地图音乐 '+str(tone))
            sky={5581:40511,104:40511,30689:40510,30691:40510,30692:40510,30693:40510,30694:40510,30695:40510}.get(int(key.split(':')[1]))
            if sky:
                info=bitmap(sky)
                if info and info.get('bmp_number')==sky:indices[str(sky)]=info;files.add('assets/'+info['file'])
                else:ref['missing'].append('专用背景 '+str(sky))
            for p in [args.client/'data/auto.dat',*sorted((args.client/'data/pal').glob('*.sap'))]:
                if p.is_file():extras['map/'+str(p.relative_to(args.client/'data'))]=p.read_bytes()
        if not files and not extras and not ref['variants']:continue
        # Spill map layouts to disk; never retain every map's layers in memory.
        if extras:
            cache=out/'source-cache'/key.replace(':','-');cache.mkdir(parents=True,exist_ok=True)
            for name,value in list(extras.items()):
                destination=cache/name;destination.parent.mkdir(parents=True,exist_ok=True);destination.write_bytes(value);extras[name]=destination
        extras_hash={k:hashlib.sha256(p.read_bytes()).hexdigest() for k,p in extras.items()}
        ref['missing']=sorted(set(ref['missing']))
        # Remove entry captions for grouping only. The UI keeps the actual captions.
        signature=hashlib.sha256(encoded({'files':sorted(files),'variants':[{k:v for k,v in variant.items() if k!='name'} for variant in ref['variants']],'images':[{k:v for k,v in im.items() if k!='caption'} for im in ref['images']],'sounds':ref['sounds'],'missing':ref['missing'],'indices':indices,'extras':extras_hash})).hexdigest()
        if signature not in groups:groups[signature]={'ref':ref,'files':files,'extras':extras,'indices':indices,'entries':[]}
        groups[signature]['entries'].append({k:e[k] for k in ['key','kind','name','fields']})
        key_refs[key]=(signature,ref)
    print(f'Planned {len(key_refs)} entries, {len(groups)} ZIPs, {len(sprite_data)} animations; referenced missing images: {len(missing_source)}',flush=True)
    if args.plan:return
    static={};packages=[]
    site=Path('internal/gamewiki/site')
    viewer=(site/'materials.js').read_bytes();css=(site/'style.css').read_bytes()
    for number,(signature,group) in enumerate(groups.items()):
        ref=group['ref'];pack={k:p.read_bytes() for k,p in group['extras'].items()};pack.update({p:locate(p).read_bytes() for p in sorted(group['files']) if locate(p)})
        used={str(v['graphic']):sprite_data[str(v['graphic'])] for v in ref['variants']}
        pack['reference.json']=encoded(ref);pack['sprites.json']=encoded({'format':2,'sprites':{k:{'actions':s['actions']} for k,s in used.items()}})
        pack['bitmap-index.json']=encoded(group['indices']);pack['entries.json']=encoded(group['entries'])
        pack['README.md']=README.encode();pack['materials.js']=viewer;pack['style.css']=css
        pack['new-asset.template.json']=encoded({'name':'填写新素材名称','new_template_id':None,'new_graphic_id':None,'new_album_graphic_id':None,'new_map_id':None,'reference_sprites':list(used),'note':'重新分配未占用编号；保留或重设经过验证的动作时序、偏移、命中事件、音效映射与地图图块索引。'})
        frames=io.StringIO();writer=csv.writer(frames);writer.writerow(['graphic','direction','action','frame_1based','tick','file','x','y','xoffset','yoffset','event'])
        for g,s in used.items():
            for a in s['actions']:
                for i,f in enumerate(a['frames']):writer.writerow([g,a['direction'],a['action'],i+1,a['frame_ms'],'assets/'+f['file'],f['x'],f['y'],f['xoffset'],f['yoffset'],f.get('sound',0)])
        pack['frames.csv']=('\ufeff'+frames.getvalue()).encode()
        pack['preview-data.js']=b'window.MATERIAL_BUNDLE='+encoded({'reference':ref,'sprites':used})+b';\n'
        pack['preview.html']=b'''<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Material reference</title><link rel="stylesheet" href="style.css"><main style="max-width:1280px;margin:auto;padding:20px"><h1>'''+'素材制作参考'.encode()+b'''</h1><p><a href="README.md">README</a> / <a href="entries.json">entries.json</a> / <a href="frames.csv">frames.csv</a></p><article id="preview"></article></main><script src="preview-data.js"></script><script src="materials.js"></script><script>WikiMaterials.mount({},document.getElementById('preview'),{reference:MATERIAL_BUNDLE.reference,sprites:MATERIAL_BUNDLE.sprites,mediaURL:p=>p,ensureCache:async()=>{}});</script></html>'''
        pack['SHA256SUMS']=''.join(hashlib.sha256(v).hexdigest()+'  '+k+'\n' for k,v in sorted(pack.items())).encode()
        staging=out/'current.zip'
        with zipfile.ZipFile(staging,'w',compression=zipfile.ZIP_DEFLATED,compresslevel=3) as z:
            for name,raw in sorted(pack.items()):
                zi=zipfile.ZipInfo(name,(2020,1,1,0,0,0));zi.external_attr=0o644<<16;zi.compress_type=zipfile.ZIP_STORED if name.endswith(('.png','.webp')) else zipfile.ZIP_DEFLATED;z.writestr(zi,raw,compresslevel=3)
        with staging.open('rb') as stream: digest=hashlib.file_digest(stream,'sha256').hexdigest()
        dest=out/(digest+'.zip');staging.replace(dest)
        download={'path':'wiki/materials/'+dest.name,'bytes':dest.stat().st_size,'sha256':digest}
        group['download']=download;packages.append(download)
        if number%100==0:print(f'Packed {number+1}/{len(groups)}',flush=True)
    for key,(signature,ref) in key_refs.items():
        ref=dict(ref,download=groups[signature]['download']);shard=hashlib.sha256(key.encode()).hexdigest()[:2];static.setdefault(shard,{})[key]=ref
    blobs={k+'.json':encoded(v) for k,v in static.items()}
    for i in range(256):blobs.setdefault(f'{i:02x}.json',b'{}')
    blobs.update({'sprite-'+k+'.json':encoded(v) for k,v in sprite_data.items()})
    revision='materials-'+hashlib.sha256(b''.join(blobs[k] for k in sorted(blobs))).hexdigest()[:16]
    metadata=[]
    for name,raw in blobs.items():
        file=out/'metadata'/revision/(name+'.gz');write(file,raw)
        payload=file.read_bytes();metadata.append({'path':'wiki/materials/'+revision+'/'+name,'file':str(file.relative_to(out)),'bytes':len(payload),'sha256':hashlib.sha256(payload).hexdigest()})
    pointer=encoded({'revision':revision})
    (out/'catalog.json').write_bytes(pointer)
    publication={'format':1,'revision':revision,'packages':packages,'metadata':metadata};(out/'materials-publication.json').write_bytes(encoded(publication))
    print(json.dumps({'revision':revision,'entries':len(key_refs),'packages':len(packages),'zip_bytes':sum(p['bytes'] for p in packages),'missing_images':sorted(missing_source)},ensure_ascii=False),flush=True)


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--assets',type=Path,default=Path('client/web/assets/original'))
    parser.add_argument('--snapshot',type=Path,default=Path('internal/gamewiki/snapshot'))
    parser.add_argument('--data',type=Path,default=Path('server/legacy/source/2.5/gmsv/data'))
    parser.add_argument('--client',type=Path,default=Path('runtime/legacy-client'))
    parser.add_argument('--maps',type=Path,default=Path('build/wiki-media-final/maps'))
    parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--cdn',default='')
    parser.add_argument('--keys',nargs='*')
    parser.add_argument('--plan',action='store_true')
    build(parser.parse_args())
