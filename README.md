# Localizer — personnes vs lieux, image vs mot (MEG)

Construit sur le modèle de `MEG-pronouns-main` : même version de goxpyriment, même
installation MEG (port parallèle, boutons verts, photodiode), même format de données.

## Récupérer le programme

Les exécutables déjà compilés sont dans `bin/` (les photos sont intégrées dedans,
ils fonctionnent seuls, sans le reste du dossier) :
- `bin/Localizer.exe` : Windows, pour tester ;
- `bin/localizer` : Linux, machine MEG (`chmod +x localizer` si besoin).

Les copier où l'on veut ; les données sont écrites dans `data/` à côté de l'exécutable.

## Lancer

| Où | Commande |
|---|---|
| PC Windows (test, fenêtre, clavier) | `.\Localizer.exe -w -s 1` (ESPACE quand le cadre est rouge) |
| Test rapide | `.\Localizer.exe -w -s 1 -n 40` (40 essais par run) |
| Machine MEG (Linux) | `./localizer -s 1 -meg` |
| Reprendre au run 2 | `./localizer -s 1 -meg -run 2` |
| Contrôle de timing sans participant | `./localizer -s 1 -meg -auto` |

| Option | Rôle |
|---|---|
| `-s N` | sujet (l'ordre des essais dépend du sujet, et il est reproductible) |
| `-meg` | triggers sur `/dev/parport1` ; réponses avec les boutons verts (l'un ou l'autre : gauche `/dev/parport0` masque 0x08, droit `/dev/parport1` masque 0x20) |
| `-run N` | commencer au run N (1 ou 2) |
| `-practice=false` | sans les 16 essais d'entraînement (faits avant le run 1, sans triggers) |
| `-photodiode on/off/auto` | carré blanc en haut à gauche à chaque onset (`auto` = actif avec `-meg`) |
| `-n N` | seulement les N premiers essais de chaque run (tests) |
| `-auto` | runs lancés sans ESPACE, réponses simulées (contrôle du timing) |
| `-o DOSSIER` | dossier des données (défaut : `data` à côté de l'exécutable) |
| `-w` | fenêtré (jamais pour un enregistrement) |

ESPACE démarre chaque run (l'opérateur). ESC arrête tout ; ce qui a été présenté est sauvegardé.

## Déroulé

1. Consignes, puis 16 essais d'entraînement (3 cadres rouges).
2. Deux runs de 384 essais, environ 6,5 minutes chacun, avec une pause entre les deux.
3. Chaque run commence par 2 s de point de fixation.
4. Chaque essai dure 1 s : l'item reste 500 ms dans un cadre de 360 px, puis le point de fixation seul reste 500 ms.
5. Les essais alternent strictement image et mot. Le premier format change d'un sujet à l'autre et d'un run à l'autre.
6. Dans chaque run, chaque item (16 métiers et 16 lieux) passe 6 fois en photo et 6 fois en mot.
7. Contraintes d'ordre :
   - un même item ne revient jamais à moins de 2 essais d'écart ;
   - il n'y a jamais plus de 4 personnes, ni plus de 4 lieux, d'affilée.
8. Les photos d'un même item sont utilisées à tour de rôle sur l'ensemble de la session.
9. Tâche de vigilance : 20 cadres rouges par run.
   - Ils se répartissent en 10 photos et 10 mots, avec autant de personnes que de lieux.
   - Aucun cadre rouge pendant les 10 premiers essais, et au moins 8 essais entre deux cadres rouges.
   - Le participant appuie dès qu'il voit un cadre rouge. Un appui dans la seconde qui suit compte comme une détection ; tout autre appui est une fausse alarme.

## Triggers (STI101, ou STI001–STI008)

| Code | Événement |
|---|---|
| 1–32 | photo, cadre blanc (code = n° de l'item) |
| 33–64 | mot, cadre blanc (32 + n° de l'item) |
| 65–96 | photo, cadre rouge (64 + n° de l'item) |
| 97–128 | mot, cadre rouge (96 + n° de l'item) |
| 201, 202 | écran de consignes ou de pause avant le run 1 ou 2 |
| 255 | écran de fin |

Les numéros d'items sont dans `items.csv` : 1–16 pour les personnes (8 hommes puis 8 femmes), 17–32 pour les lieux.

Pour retrouver les catégories à partir du code :
- code ≤ 16 ou 33–48 : personne en blanc ;
- 17–32 ou 49–64 : lieu en blanc.

Chaque code part juste après le flip qui affiche l'item, en même temps que le carré de la photodiode. Les boutons sont enregistrés par la MEG sur leurs propres lignes STI. L'entraînement n'envoie aucun trigger.

Le mot affiché est le nom seul, sans article (« stade », « cave »), comme le nom isolé dans les phrases de la tâche principale.

## Photos

| Fichier ou dossier | Contenu |
|---|---|
| `assets/images_raw/` | photos d'origine |
| `assets/images/` | photos normalisées par `python normalize_images.py`, intégrées à l'exécutable |
| `assets/images/normalisation.csv` | détail de la normalisation, image par image |
| `assets/images/contact.jpg` | planche contact des photos normalisées |
| `assets/images_raw/crop_focus.csv` | recadrages corrigés à la main |

La normalisation fait trois choses :
1. elle recadre chaque photo en carré de 320 × 320 px, sans la déformer ;
2. elle ramène toutes les photos à la même luminance moyenne et au même contraste ;
3. elle ne touche que la luminance : les couleurs sont conservées.

**Après une modification des photos, il faut relancer `normalize_images.py`, puis recompiler**, car les photos sont intégrées à l'exécutable.

## Données

Les fichiers sont écrits dans `data/Localizer_sub-NNN_date-….csv`, avec un `-info.txt` qui contient la configuration et les scores de chaque run.

Le CSV contient une ligne par événement :

| Événement | Moment |
|---|---|
| `pause` | écran de consignes ou de pause |
| `fixation` | point de fixation au début du run |
| `practice` | essai d'entraînement |
| `stimulus` | essai du run |
| `press` | appui qui détecte un cadre rouge |
| `false_alarm` | appui hors fenêtre de détection |
| `end` | écran de fin |

Pour chaque essai, les colonnes donnent :
- le run, le numéro d'essai, le format, la catégorie, l'item, le mot et le fichier photo ;
- s'il s'agit d'un cadre rouge, et le code envoyé ;
- l'instant mesuré de l'affichage et l'instant prévu ;
- la durée réelle à l'écran ;
- pour les cadres rouges, le résultat (`hit` ou `miss`) et le temps de réaction.

## Recompiler

Sous Windows, comme pour `MEG-pronouns-main` :

```powershell
$env:GOWORK="off"; $env:GONOSUMDB="github.com/chrplr/goxpyriment"; $env:GOPROXY="direct"
go build -o Localizer.exe .
```

Pour la machine MEG (Linux), depuis Windows :

```powershell
$env:GOOS="linux"; go build -o localizer . ; $env:GOOS=""
```

Le `$env:GOWORK="off"` n'est nécessaire que si le dossier se trouve à l'intérieur d'un dépôt goxpyriment.
Sous Linux/macOS : `go build -o localizer .` (même remarque pour `GONOSUMDB`/`GOPROXY` si le checksum pose problème).

## Avant la première vraie séance

- Lancer `./localizer -s 1 -meg -auto -n 40` et vérifier :
  - que les codes arrivent bien sur STI101 ;
  - le délai entre le trigger et la photodiode ;
  - que les deux boutons verts apparaissent bien sur leurs canaux (STI009 et STI014).
- Boutons : les verts (gauche = port 0 valeur expyriment 0b0001 = bit status 0x08, STI009 ; droit = port 1 0b0100 = 0x20, STI014) viennent du tableau de la salle MEG (19/10/2026). Ils sont hors des lignes des triggers (STI001–STI008). Le bleu gauche partageait STI007 avec le bit 64 des triggers. `python3 tools/test_boutons_parport.py` affiche les lignes des ports en direct pour vérifier.
