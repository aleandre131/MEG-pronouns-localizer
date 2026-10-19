# Boutons verts pour la tâche principale (MEG-pronouns de Barbu)

Remplace les boutons rouges par les boutons verts dans `main.go` :
- vert gauche : `/dev/parport0`, valeur expyriment 0b0001 = bit status 0x08 → STI009 sur la MEG ;
- vert droit : `/dev/parport1`, valeur expyriment 0b0100 = bit status 0x20 → STI014 sur la MEG ;
- triggers inchangés (`/dev/parport1`, lignes de données = STI001–STI008) ; consignes « bouton vert ».

Les verts sont hors des lignes des triggers. Pas de recodage nécessaire : le bleu gauche (STI007) partageait sa ligne avec le bit 64 des triggers.

Tableau de la salle (19/10/2026), convention expyriment (status >> 3) :

| Bouton | Port | Valeur | Masque status | Ligne MEG |
|---|---|---|---|---|
| vert gauche | 0 | 0b0001 | 0x08 | STI009 |
| vert droit | 1 | 0b0100 | 0x20 | STI014 |
| bleu gauche | 2 | 0b1000 | 0x40 | STI007 (conflit trigger) |
| bleu droit | 0 | 0b1000 | 0x40 | STI012 |
| rouge gauche | 0 | 0b0100 | 0x20 | — |
| rouge droit | 1 | 0b0010 | 0x10 | — |

## Appliquer (dans le dossier de MEG-pronouns, celui qui contient main.go)

Sur le `main.go` d'origine de Barbu (boutons rouges) :

```bash
patch -p1 < ../MEG-pronouns-localizer/patch-pronouns/boutons-verts.patch
go build -o pronouns .
```

Si le patch bleu a déjà été appliqué, ou si `patch` refuse, remplace simplement `main.go` par le fichier complet `patch-pronouns/main.go` (version du 15/10), puis `go build -o pronouns .`.
