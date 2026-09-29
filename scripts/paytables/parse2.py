import re,sys,json,html as H
COLS=[0,2,3,4,6,8,10,12,14,16,18,20,22,24,26,28,30,32,34,36,38,40]
GRADES=[f'E-{i}' for i in range(1,10)]+[f'W-{i}' for i in range(1,6)]+[f'O-{i}E' for i in (1,2,3)]+[f'O-{i}' for i in range(1,11)]
def col(label):
    l=label.lower()
    if re.search(r'(2 or less|less than 2|under 2|<\s*2|^2 or fewer)', l): return 0
    m=re.search(r'over\s*(\d+)', l)
    return int(m.group(1)) if m else None
def grade(label):
    m=re.match(r'\s*([EWO])\s*-?\s*(\d{1,2})\s*(E)?\b', label)
    if not m: return None
    g=f'{m.group(1)}-{m.group(2)}'+('E' if m.group(3) else '')
    return g if g in GRADES else None
def cents(v):
    v=v.replace('$','').replace(',','').strip()
    return int(round(float(v)*100)) if re.fullmatch(r'\d+(\.\d+)?',v) else None
def parse(path):
    s=open(path,encoding='utf-8',errors='ignore').read()
    out={}
    for tb in re.findall(r'<table.*?</table>', s, re.S|re.I):
        hdr=None
        for tr in re.findall(r'<tr[^>]*>(.*?)</tr>', tb, re.S|re.I):
            cells=[H.unescape(re.sub(r'<[^>]+>',' ',c)).strip() for c in re.findall(r'<t[dh][^>]*>(.*?)</t[dh]>', tr, re.S|re.I)]
            if not cells: continue
            cs=[col(c) for c in cells[1:]]
            if sum(c is not None for c in cs)>=3:
                hdr=cs if col(cells[0]) is None else [col(c) for c in cells]
                continue
            g=grade(cells[0])
            if g and hdr:
                for c,v in zip(hdr,cells[1:]):
                    if c is None: continue
                    x=cents(v)
                    if x and c not in out.setdefault(g,{}): out[g][c]=x  # first table wins: active duty comes before drill pay
    return out
if __name__=='__main__':
    for p in sys.argv[1:]:
        t=parse(p); print(p, len(t),'grades; E-8', {k:t.get('E-8',{}).get(k) for k in (16,18,20,22)})
