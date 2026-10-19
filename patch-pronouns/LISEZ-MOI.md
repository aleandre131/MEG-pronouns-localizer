# Boutons bleus pour la tâche principale (MEG-pronouns de Barbu)

Remplace les boutons rouges par les boutons bleus dans `main.go` :
- bleu gauche : `/dev/parport2`, status bit 6 (0x40) = valeur expyriment 0b1000, port 2 ;
- bleu droit : `/dev/parport0`, status bit 6 (0x40) = valeur expyriment 0b1000, port 0 ;
- triggers inchangés (`/dev/parport1`) ; consignes « bouton bleu ».

(Rouges, pour mémoire : port 0 0b0100 = 0x20 sur parport0, port 1 0b0010 = 0x10 sur parport1.)

## Appliquer (dans le dossier de MEG-pronouns, celui qui contient main.go)

```bash
git apply ../MEG-pronouns-localizer/patch-pronouns/boutons-bleus.patch
go build -o pronouns .
```

Si `git apply` refuse (version de Barbu différente de celle du 15/10), ne rien forcer : envoyer le message d'erreur.
Le fichier complet modifié (version du 15/10) est aussi là : `patch-pronouns/main.go`.
