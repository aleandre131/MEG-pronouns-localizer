#!/usr/bin/env python3
"""Affiche en direct les lignes des ports paralleles, pour trouver sur quelle
ligne arrive chaque bouton de la boite de reponse MEG.

Usage (machine MEG, Linux) :
    python3 tools/test_boutons_parport.py            # tous les /dev/parport*
    python3 tools/test_boutons_parport.py /dev/parport0 /dev/parport1

Appuyer sur UN bouton a la fois (bleu gauche, bleu droit, rouge gauche...),
le maintenir 1 seconde, le relacher. A chaque changement, le script affiche
le registre qui a bouge, les bits qui ont change et la broche correspondante.
Ctrl+C pour quitter. N'ecrit RIEN sur les ports (lecture seule).

Prerequis : sudo modprobe ppdev ; sudo rmmod lp ; etre dans le groupe lp
(ou lancer avec sudo).
"""
import fcntl
import glob
import os
import struct
import sys
import time

# ioctl ppdev (linux/ppdev.h)
PPCLAIM = 0x708B
PPRELEASE = 0x708C
PPRSTATUS = 0x80017081
PPRCONTROL = 0x80017083
PPRDATA = 0x80017085

# bit -> broche DB25, pour chaque registre
PINS = {
    "status": {3: "15 (nError)", 4: "13 (Select)", 5: "12 (PaperOut)", 6: "10 (nAck)", 7: "11 (Busy, inversee)"},
    "control": {0: "1 (nStrobe, inversee)", 1: "14 (nAutoFd, inversee)", 2: "16 (nInit)", 3: "17 (nSelectIn, inversee)"},
    "data": {i: f"{i + 2} (D{i})" for i in range(8)},
}
REQ = {"status": PPRSTATUS, "control": PPRCONTROL, "data": PPRDATA}


def read(fd, reg):
    buf = bytearray(1)
    fcntl.ioctl(fd, REQ[reg], buf, True)
    return buf[0]


def main():
    devs = sys.argv[1:] or sorted(glob.glob("/dev/parport*"))
    if not devs:
        sys.exit("Aucun /dev/parport* : lancer 'sudo modprobe ppdev' puis recommencer.")
    fds = {}
    for d in devs:
        try:
            fd = os.open(d, os.O_RDWR)
            fcntl.ioctl(fd, PPCLAIM)
            fds[d] = fd
            print(f"ouvert : {d}")
        except OSError as e:
            print(f"impossible d'ouvrir {d} : {e}  (groupe lp ? 'sudo rmmod lp' ?)")
    if not fds:
        sys.exit(1)
    last = {(d, r): read(fd, r) for d, fd in fds.items() for r in REQ}
    print("\nEtat de repos :")
    for (d, r), v in sorted(last.items()):
        print(f"  {d:14s} {r:8s} 0x{v:02X}  {v:08b}")
    print("\nAppuyez sur un bouton a la fois (Ctrl+C pour quitter)...\n")
    try:
        while True:
            for d, fd in fds.items():
                for r in REQ:
                    v = read(fd, r)
                    old = last[(d, r)]
                    if v != old:
                        diff = v ^ old
                        bits = [b for b in range(8) if diff >> b & 1]
                        desc = ", ".join(
                            f"bit {b} (masque 0x{1 << b:02X}) {'1' if v >> b & 1 else '0'} -> broche {PINS[r].get(b, '?')}"
                            for b in bits)
                        print(f"{time.strftime('%H:%M:%S')}  {d}  {r:8s} 0x{old:02X} -> 0x{v:02X}   {desc}")
                        last[(d, r)] = v
            time.sleep(0.002)
    except KeyboardInterrupt:
        pass
    finally:
        for fd in fds.values():
            try:
                fcntl.ioctl(fd, PPRELEASE)
            except OSError:
                pass
            os.close(fd)


if __name__ == "__main__":
    main()
