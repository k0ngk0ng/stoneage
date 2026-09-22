#!/usr/bin/env python3
"""Build encyclopedia media references and bounded-size map images offline.
Uses the client's verified PNG manifest and the server's LS2MAP tile layers.
No native game data or rendering is needed on the production host.
"""
import argparse, gzip, hashlib, json, re, struct
from collections import Counter
from pathlib import Path
from PIL import Image


def build(args):
    root, assets, out = args.data, args.assets, args.output
    out.mkdir(parents=True, exist_ok=True)
    manifest = json.loads((assets/'manifest.json').read_text())
    sprites = json.loads((assets/'sprites.json').read_text())['sprites']
    catalog = json.load(gzip.open(args.snapshot/'catalog.json.gz'))
    entries = {}
    for p in (args.snapshot/catalog['revision']).glob('??.json.gz'):
        entries.update(json.load(gzip.open(p)))
    bitmaps, aliases = manifest['bitmaps'], manifest.get('bitmap_aliases', {})
    def bitmap(number):
        k = str(number)
        return bitmaps.get(str(aliases.get(k,k))) or bitmaps.get(k)
    def picture(number, caption):
        sprite = sprites.get(str(number))
        if sprite:
            actions = sprite['actions']
            action = next((a for a in actions if a['action']==0 and a['direction']==1),None)
            action = action or next((a for a in actions if a['action']==0),actions[0])
            file = action['frames'][0]['file']
        else:
            info = bitmap(number)
            if not info:return None
            file = info['file']
        if not (assets/file).is_file():return None
        return {'path':'assets/'+file,'caption':caption}
    result = {}
    reusable = json.loads(args.reuse_maps.read_text()) if args.reuse_maps else {}
    rebuild = set(args.rebuild_maps)
    for k,e in entries.items():
        fields = {f['label']:f['value'] for f in e['fields'] or []}
        number = fields.get('图号')
        image = picture(number,e['name']) if number else None
        result[k]={'images':[image] if image else []}
    # Reuse actual related creature art for enemy and encounter entries.
    for kind in ['enemy','battle_npc','encounter','quest','server_task','npc_template']:
        for k,e in entries.items():
            if e['kind']!=kind:continue
            images=result[k]['images']
            for link in e['links'] or []:
                related=entries.get(link.get('key'))
                if related and related['kind'] in ['pet','enemy','item','equipment']:
                    images.extend(result[related['key']]['images'])
            if kind=='quest':
                # Exact known names in the documented reward/steps, never guessed art.
                text=' '.join(f['value'] for f in e['fields'] or [])+' '+json.dumps(e['tables'],ensure_ascii=False)
                candidates=[p for p in entries.values() if p['kind']=='pet' and len(p['name'])>=2 and re.search(r'(?<![\u4e00-\u9fff])'+re.escape(p['name'])+r'(?![\u4e00-\u9fff])',text)]
                for p in candidates:images.extend(result[p['key']]['images'])
            result[k]['images']=list({p['path']:p for p in images}.values())[:8]
    missing=Counter()
    for index,(k,e) in enumerate((x for x in entries.items() if x[1]['kind']=='map')):
        source=root/e['sources'][0]
        raw=source.read_bytes()
        if raw[:6]!=b'LS2MAP':raise ValueError(source)
        w,h=struct.unpack_from('>HH',raw,40);cells=w*h
        layers=struct.unpack_from('>'+str(cells*2)+'H',raw,44)
        missing.update(number for number in set(layers) if number>99 and
                       (not bitmap(number) or not (assets/bitmap(number)['file']).is_file()))
        if k in reusable and reusable[k].get('map') and int(k.split(':')[1]) not in rebuild:
            result[k] = reusable[k]
            continue
        # Maximum RGBA canvas is 4096 x 4096, regardless of island dimensions.
        cw,ch=(w+h)*32+512,(w+h)*24+632
        scale=min(1,4096/max(cw,ch));size=(round(cw*scale),round(ch*scale))
        ox,oy=256,(w-1)*24+256
        cache={}
        canvas=Image.new('RGBA',size,(0,0,0,0))
        for layer in range(2):
            # Same reverse diagonal submission order as asset_cooker.render_isometric.
            for delta in range(-(w-1),h):
                for x in range(max(0,-delta),min(w,h-delta)):
                    y=x+delta;number=layers[layer*cells+y*w+x]
                    if number<=99:continue
                    if number not in cache:
                        info=bitmap(number)
                        if not info or not (assets/info['file']).is_file():cache[number]=None
                        else:
                            with Image.open(assets/info['file']) as im:
                                im=im.convert('RGBA'); im=im.resize((max(1,round(im.width*scale)),max(1,round(im.height*scale))),Image.Resampling.LANCZOS)
                            cache[number]=(im,info.get('xoffset',0),info.get('yoffset',0))
                    tile=cache[number]
                    if tile:
                        im,dx,dy=tile
                        canvas.alpha_composite(im,(round((ox+(x+y)*32+dx)*scale),round((oy+(y-x)*24+dy)*scale)))
        bounds=canvas.getbbox() or (0,0,*size)
        left,top,right,bottom=bounds
        canvas=canvas.crop(bounds)
        background=Image.new('RGBA',canvas.size,(244,229,193,255));background.alpha_composite(canvas);canvas=background
        size=canvas.size
        def point(x,y):return ((ox+(x+y)*32)*scale-left)/size[0],((oy+(y-x)*24)*scale-top)/size[1]
        floor=k.split(':')[1]
        target=out/(floor+'.webp')
        canvas.convert('RGB').save(target,'WEBP',quality=85,method=4)
        digest=hashlib.sha256(target.read_bytes()).hexdigest()[:16]
        name=f'{floor}-{digest}.webp';target.rename(out/name)
        thumb=canvas.copy();thumb.thumbnail((320,240));thumb.save(out/('thumb-'+name),'WEBP',quality=80)
        markers=[]
        for link in e['links'] or []:
            if not link.get('key','').startswith(('npc:','battle_npc:')):continue
            match=re.search(r'\((\d+), (\d+)\)',link['name'])
            if not match:continue
            x,y=map(int,match.groups())
            if 0<=x<w and 0<=y<h:
                markers.append({'key':link['key'],'name':entries[link['key']]['name'],'x':point(x,y)[0],'y':point(x,y)[1],'kind':'npc'})
        for t in e['tables'] or []:
            if t['title']!='传送出口':continue
            for row in t['rows']:
                pos=re.search(r'\((\d+), (\d+)\)',row[0]); dest=re.search(r'\[(\d+)\]',row[1])
                if pos and dest:
                    x,y=map(int,pos.groups())
                    if 0<=x<w and 0<=y<h:markers.append({'key':'map:'+dest[1],'name':row[1].split(' [')[0],'x':point(x,y)[0],'y':point(x,y)[1],'kind':'exit'})
        result[k]={'images':[{'path':'wiki/maps/thumb-'+name,'caption':e['name']}],'map':{'path':'wiki/maps/'+name,'width':size[0],'height':size[1],'markers':markers}}
        if index%40==0:print('rendered',index+1,'maps',flush=True)
    enrich_related(result,entries)
    args.manifest.write_text(json.dumps(result,ensure_ascii=False,separators=(',',':'))+'\n')
    print('coverage',Counter(entries[k]['kind'] for k,v in result.items() if v['images']))
    print('unmapped logical map art',len(missing))
    (out.parent/'map-art-missing.json').write_text(json.dumps(missing,sort_keys=True))
    provenance={str(p.relative_to(assets)):hashlib.sha256(p.read_bytes()).hexdigest() for p in [assets/'manifest.json',assets/'sprites.json']}
    provenance['map_sources']={e['sources'][0]:hashlib.sha256((root/e['sources'][0]).read_bytes()).hexdigest() for e in entries.values() if e['kind']=='map'}
    (out.parent/'provenance.json').write_text(json.dumps(provenance,sort_keys=True,indent=2)+'\n')


def enrich_related(result,entries):
    quest_maps={'n2':20801,'n4':20401,'n8':3400,'n11':10901,'b3':2000,'b4':11201,'b7':10001,'z1':21001,'z3':21201,'z5':32001,'wt02-food-C':2000,'wt02-food-F':2000,'wt04-food-C':4000,'wt04-food-E':4000}
    quest_maps.update({'j1':31701,'j2':31901,'j5':31601})
    quest_maps.update({'n3': 3300, 'n12': 21001, 'n5': 21001, 'n13': 2000, 'z2': 11201, 'sa25_03': 2000, 'n1': 3100, 'n7': 3305, 'faq01': 2008, 'b6': 1400, 'sa25_04': 30619, 'news_01': 1000, 'b8': 10701, 'j3': 30600, 'wt03-food-A': 3000, 'wt03-food-B': 3000, 'wt03-food-C': 3000, 'wt03-food-D': 3000, 'wt03-food-E': 3000, 'wt03-food-F': 3000})
    for quest,floor in quest_maps.items():
        key='quest:'+quest;mapkey='map:'+str(floor)
        if key in result and not result[key]['images'] and mapkey in result:
            image=dict(result[mapkey]['images'][0]);image['caption']='任务地点：'+entries[mapkey]['name'];result[key]['images']=[image]
    for key,entry in entries.items():
        if result[key]['images']:continue
        if entry['kind'] in ('npc','encounter'):
            for link in entry['links'] or []:
                if link.get('key','').startswith('map:') and result.get(link['key'],{}).get('images'):
                    image=dict(result[link['key']]['images'][0]);image['caption']='所在地图：'+entries[link['key']]['name'];result[key]['images']=[image];break
        if entry['kind']=='quest':
            text=entry['name']+' '+entry['description']+' '+json.dumps(entry['fields'],ensure_ascii=False)+' '+json.dumps(entry['tables'],ensure_ascii=False)
            candidates=[e for e in entries.values() if e['kind'] in ('equipment','item','map') and len(e['name'])>=3 and e['name'] in text and result[e['key']]['images']]
            candidates.sort(key=lambda e:-len(e['name']))
            seen=set()
            for e in candidates:
                image=result[e['key']]['images'][0]
                if image['path'] not in seen:result[key]['images'].append(image);seen.add(image['path'])
                if len(seen)>=4:break
        if entry['kind']=='skill':
            candidates=[e for e in entries.values() if e['kind']=='pet' and entry['name']+' [' in e.get('skills','') and result[e['key']]['images']]
            if candidates:
                image=dict(result[candidates[0]['key']]['images'][0]);image['caption']='拥有此技能的宠物：'+candidates[0]['name'];result[key]['images']=[image]

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--data',type=Path,default=Path('server/legacy/source/2.5/gmsv/data'))
    p.add_argument('--assets',type=Path,default=Path('client/web/assets/original'))
    p.add_argument('--snapshot',type=Path,default=Path('internal/gamewiki/snapshot'))
    p.add_argument('--output',type=Path,required=True)
    p.add_argument('--manifest',type=Path,required=True)
    p.add_argument('--reuse-maps',type=Path,help='Reuse unchanged map previews from a previous media manifest')
    p.add_argument('--rebuild-maps',type=int,nargs='*',default=[],help='Map IDs whose source or tiles changed; rebuild despite --reuse-maps')
    build(p.parse_args())
