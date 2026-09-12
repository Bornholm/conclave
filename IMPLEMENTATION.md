# Plan d'implémentation révisé

Ce document amende `PLAN.md`. Les sections non mentionnées restent valables telles quelles.

## Amendements

| # | Sujet | Décision |
|---|-------|----------|
| 1 | Sortie des agents | Extracteur tolérant : JSON brut, enveloppe Claude (`structured_output`, puis `result`), NDJSON, bloc ```json, scan d'accolades vers un objet portant `schema_version`. |
| 2 | Transmission du prompt | Champ `input: stdin \| argument \| file` par agent. `file` remplace `{prompt_file}` dans la commande. |
| 3 | Environnement | `inherit_env: true` par défaut. Variables `GIT_*` dangereuses toujours retirées. `NO_COLOR=1`, `TERM=dumb`, `CI=1` ajoutées. Variables protégées (`PATH`, `HOME`, `GIT_*`) interdites dans `environment` sauf `allow_protected_env: true`. |
| 4 | Fetch Git | `refs/pull/<n>/head` depuis le remote configuré, vérification du SHA, repli sur fetch direct du SHA depuis le `clone_url`. Base : fetch du SHA depuis le remote. |
| 5 | Durées | `time.Duration` décodé nativement par `yaml.v3`. |
| 6 | Processus | `Setpgid`, `cmd.Cancel` tue le groupe, `WaitDelay` 5s. |
| 7 | Énumérations | Sévérités `critical, high, medium, low, info`. Catégories normalisées, `other` en repli. |
| 8 | Diff volumineux | Troncature à `max_diff_bytes` avec marqueur `[diff truncated]`. |
| 9 | CLI | `flag` de la bibliothèque standard. Dépendances : `gopkg.in/yaml.v3`, `golang.org/x/sync`. |
| 10 | Artefacts | `<git-common-dir>/conclave/runs/<run-id>/`. Worktrees dans `os.TempDir()/conclave/<run-id>/<agent-id>`. |
| 12 | Modèle | Champ `model` par agent (étiquette). La valeur auto-déclarée par l'agent est ignorée ; sinon détection depuis l'enveloppe Claude (`modelUsage`) ou les événements Pi. Rendu dans `meta.models`, le manifeste et le Markdown. |
| 13 | Pi | `output: pi-json` pour `pi --mode json` : extraction du dernier message assistant, trace des outils dans `raw/<id>.trace.jsonl`, `max_output_bytes` par agent. |
| 14 | Spécialités | Optionnelles, formulées comme zones de priorité et non comme périmètre. Le prompt exige une revue complète. Retirées de la configuration d'exemple. |
| 15 | Hors périmètre | Un finding dans un fichier non modifié par la PR est conservé avec `out_of_scope: true` (recalculé par Conclave, jamais laissé au lead), rendu dans une section dédiée, exclu du verdict et du regroupement avec les findings en périmètre. |
| 16 | Lead conservateur | Prompt lead avec politique explicite : ne supprimer qu'après vérification dans le code, ne pas abaisser une sévérité multi-reviewers sans preuve, juger l'état après fusion et non le progrès relatif, règles de verdict fixes, verdict le plus strict en cas d'hésitation. Le fallback déterministe force `request_changes` sur un finding high/critical en périmètre. |
| 17 | Répertoire de travail | Le prompt nomme le worktree absolu comme seul répertoire autorisé et demande d'ignorer toute autre "racine de projet" annoncée par l'outil (OpenCode remonte au dépôt principal via le `.git` commun). Placeholder `{worktree}` dans les commandes. |
| 18 | Discussion et issues | Récupération des commentaires, reviews et commentaires inline (GitHub et Gitea), de toute issue référencée `#N` dans la description ou la discussion (plafond `max_issues`), bornés par `max_comments` et `max_comment_bytes`. Bloc "DISCUSSION" et "CONTEXT RULES" dans les prompts : le contexte non fiable reste la source du périmètre déclaré ; report explicite vers un ticket → info ; point déjà soulevé par un humain → marqué confirmé ; pas de question déjà répondue. |
| 11 | Lead | Validation stricte : `reported_by` ⊆ reviewers réussis, sinon finding rejeté. Fallback déterministe si échec. |

## Lots

1. **Fondation** : `go.mod`, `config` (types, défauts, chargement strict, validation), `gitrepo/remote` (parsing URL), `cli` (`version`, `config validate`, `agents check`, squelette `review`).
2. **Domaine et HTTP** : `domain` (PR, rapports, run), `forge/httpx` (client REST, auth par hôte, limite de taille, pagination Link / X-HasMore, erreurs typées).
3. **GitHub** : PR, fichiers paginés, issues référencées, mapping.
4. **Gitea** : idem, base_url avec sous-chemin.
5. **Git** : `Git` runner exec sans shell, `EnsureCommit`, `MergeBase`, `Diff`, worktrees.
6. **Agent** : `process` runner (limites, timeout, pgid), `prompt` (reviewer/lead), `agent` (exécution, extraction, validation), `artifact` store.
7. **Parallélisme** : `errgroup` avec limite, ordre stable, `fail_fast`.
8. **Consolidation** : normalisation, regroupement (fichier + chevauchement de lignes + catégorie + similarité de titre), prompt lead, validation, fallback.
9. **Sorties et qualité** : Markdown, JSON, README, `.conclave.example.yaml`, test bout-en-bout avec faux agent, `go vet`, `go test -race`.
