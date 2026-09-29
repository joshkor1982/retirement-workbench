import re,html as H
from parse2 import GRADES, col, grade
def parse(path):
    # NavyCS publishes whole dollars in split tables with "Over N" headers.
    s=open(path,encoding='utf-8',errors='ignore').read(); out={}
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
                    v=v.replace('$','').replace(',','').strip()
                    if c is not None and re.fullmatch(r'\d+',v) and c not in out.setdefault(g,{}): out[g][c]=int(v)*100
    return out
