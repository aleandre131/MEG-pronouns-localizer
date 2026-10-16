#!/usr/bin/env python3
"""Normalise les photos brutes du localizer.

Entree : assets/images_raw/{persons,places}/<item>/*.{jpg,jpeg,png,bmp,webp,avif}
Sortie : assets/images/{persons,places}/<item>/1.jpg, 2.jpg, ...

Pour chaque photo :
  1. orientation EXIF appliquee, transparence fondue sur du gris moyen ;
  2. recadrage CARRE sans deformation (paysage : centre ; portrait : biaise
     vers le haut pour garder les visages), redimensionnement en SIZE x SIZE.
     Un recadrage peut etre recentre a la main dans assets/images_raw/crop_focus.csv
     (colonnes category,item,file,x,y ; x,y entre 0 et 1 = centre voulu) ;
  3. egalisation de bas niveau sur TOUT le lot : chaque image est ramenee a la
     meme luminance moyenne et au meme contraste RMS de luminance (moyennes du
     lot). Seul le canal de luminance (Y de YCbCr) est modifie : la couleur
     (Cb, Cr) est conservee telle quelle.
     --no-equalize pour la desactiver.

Ecrit aussi assets/images/normalisation.csv (luminance/contraste avant/apres,
% de pixels satures) et une planche contact assets/images/contact.jpg.

Usage : python normalize_images.py [--size 320] [--no-equalize]
"""
import argparse, csv, sys
from pathlib import Path
import numpy as np
from PIL import Image, ImageOps, ImageDraw

HERE = Path(__file__).resolve().parent
RAW, OUT = HERE / "assets/images_raw", HERE / "assets/images"
CATS = ("persons", "places")
EXT = {".jpg", ".jpeg", ".png", ".bmp", ".webp", ".avif"}
GREY = (128, 128, 128)

def load(p):
    im = ImageOps.exif_transpose(Image.open(p))
    if im.mode in ("RGBA", "LA") or (im.mode == "P" and "transparency" in im.info):
        im = im.convert("RGBA"); bg = Image.new("RGB", im.size, GREY); bg.paste(im, mask=im.split()[-1]); return bg
    return im.convert("RGB")

def square(im, size, focus=None, bias=0.15):
    w, h = im.size; s = min(w, h)
    if focus:
        cx, cy = focus[0] * w, focus[1] * h
        x0 = min(max(cx - s / 2, 0), w - s); y0 = min(max(cy - s / 2, 0), h - s)
    elif w >= h:
        x0, y0 = (w - s) / 2, 0
    else:
        x0, y0 = 0, (h - s) * (0 if h >= 1.3 * w else bias)
    return im.crop((round(x0), round(y0), round(x0) + s, round(y0) + s)).resize((size, size), Image.LANCZOS)

def main():
    ap = argparse.ArgumentParser(); ap.add_argument("--size", type=int, default=320)
    ap.add_argument("--no-equalize", action="store_true"); a = ap.parse_args()
    focus = {}
    fp = RAW / "crop_focus.csv"
    if fp.exists():
        for r in csv.DictReader(open(fp, encoding="utf-8")):
            focus[(r["category"], r["item"], r["file"])] = (float(r["x"]), float(r["y"]))
    imgs = []  # (cat, item, idx, src, array)
    for cat in CATS:
        for d in sorted(p for p in (RAW / cat).iterdir() if p.is_dir()):
            files = sorted(f for f in d.iterdir() if f.suffix.lower() in EXT)
            if not files:
                print(f"ATTENTION : aucune photo dans {cat}/{d.name}"); continue
            for i, f in enumerate(files, 1):
                im = square(load(f), a.size, focus.get((cat, d.name, f.name)))
                imgs.append((cat, d.name, i, f.name, np.asarray(im, dtype=np.float64)))
    ycc = [np.asarray(Image.fromarray(x.astype(np.uint8)).convert("YCbCr"), dtype=np.float64) for *_, x in imgs]
    stats = [(c[..., 0].mean(), c[..., 0].std()) for c in ycc]
    tm, ts = np.mean([m for m, _ in stats]), np.mean([s for _, s in stats])
    rows = []
    for (cat, item, i, src, x), c, (m, s) in zip(imgs, ycc, stats):
        c = c.copy()
        if not a.no_equalize:
            c[..., 0] = (c[..., 0] - m) * (ts / s) + tm
        sat = float(((c[..., 0] < 0) | (c[..., 0] > 255)).mean() * 100)
        c = np.clip(c, 0, 255).astype(np.uint8)
        y = np.asarray(Image.fromarray(c, "YCbCr").convert("RGB"))
        od = OUT / cat / item; od.mkdir(parents=True, exist_ok=True)
        Image.fromarray(y).save(od / f"{i}.jpg", quality=92)
        L2 = np.asarray(Image.fromarray(y).convert("YCbCr"), dtype=np.float64)[..., 0]
        rows.append(dict(category=cat, item=item, file=f"{i}.jpg", source=src, lum_avant=round(m, 1), contraste_avant=round(s, 1),
                         lum_apres=round(L2.mean(), 1), contraste_apres=round(L2.std(), 1), pct_satures=round(sat, 2)))
    with open(OUT / "normalisation.csv", "w", newline="", encoding="utf-8") as fh:
        w = csv.DictWriter(fh, fieldnames=list(rows[0])); w.writeheader(); w.writerows(rows)
    # planche contact
    T = 120; items = sorted({(r["category"], r["item"]) for r in rows})
    sheet = Image.new("RGB", (130 + 5 * (T + 4), len(items) * (T + 4)), (40, 40, 40)); dr = ImageDraw.Draw(sheet)
    for k, (cat, item) in enumerate(items):
        dr.text((4, k * (T + 4) + T // 2), item, fill="white")
        for j, r in enumerate([r for r in rows if (r["category"], r["item"]) == (cat, item)][:5]):
            sheet.paste(Image.open(OUT / cat / item / r["file"]).resize((T, T)), (130 + j * (T + 4), k * (T + 4)))
    sheet.save(OUT / "contact.jpg", quality=85)
    by = {c: [r for r in rows if r["category"] == c] for c in CATS}
    print(f"{len(rows)} images -> {OUT} ({a.size}x{a.size}), egalisation {'NON' if a.no_equalize else 'OUI'}: lum {tm:.1f}, contraste {ts:.1f}")
    for c in CATS:
        print(f"  {c}: {len(by[c])} images, {len({r['item'] for r in by[c]})} items, lum avant {np.mean([r['lum_avant'] for r in by[c]]):.1f} -> apres {np.mean([r['lum_apres'] for r in by[c]]):.1f}, "
              f"contraste avant {np.mean([r['contraste_avant'] for r in by[c]]):.1f} -> apres {np.mean([r['contraste_apres'] for r in by[c]]):.1f}")
    worst = sorted(rows, key=lambda r: -r["pct_satures"])[:5]
    print("  plus forte saturation :", ", ".join(f"{r['item']}/{r['source']} {r['pct_satures']}%" for r in worst))

if __name__ == "__main__":
    main()
