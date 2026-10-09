# Parkrr — Betreiber-Handbuch

Für die Person, die Parkrr BETREIBT: installiert, sichert, aktualisiert und im
Ernstfall wiederherstellt. Die Bedienung der Anwendung selbst erklärt sich in der
Oberfläche; dieses Handbuch behandelt alles außenherum. (Hundert 97)

Die README bleibt die Referenz für jede einzelne Umgebungsvariable — hier steht,
**wann man was tut und warum**.

---

## 1. Die täglichen fünf Minuten

Es gibt genau drei Stellen, die ein Betreiber regelmäßig ansieht:

1. **Dashboard → Backup-Kachel.** Grün = der letzte geplante Lauf war
   erfolgreich UND pünktlich (gemessen am eigenen Cron, nicht an einer fixen
   Frist). Rot heißt: der jüngste brauchbare Wiederherstellungspunkt altert.
2. **Dashboard → Kundenwünsche** (erscheint nur, wenn offen): Stammdaten-Wünsche
   übernehmen oder ablehnen, Abholtermine bestätigen.
3. **Audit-Log** (Admins): das Änderungsprotokoll. Der Knopf „E-Mail-Versand"
   daneben beantwortet „ist die Mahnung rausgegangen?", der „Revisionsexport"
   liefert das Protokoll als prüfbare Datei (SHA-256-Hashkette, siehe §6).

Wer `PARKRR_ALERT_EMAIL` gesetzt hat, bekommt Punkt 1 als Mail, wenn ein
geplantes Backup fehlschlägt — dann reicht der Blick in den Posteingang.

## 2. Backups: einrichten, prüfen, wiederherstellen

**Einrichten:** `PARKRR_BACKUP_KEY` setzen (mindestens 32 Zeichen, getrennt vom
Session-Secret aufbewahren!) und ein Ziel: `PARKRR_BACKUP_DIR` (gemountetes
Volume) und/oder `PARKRR_S3_*`. Zeitplan und Aufbewahrung stellt man danach in
der Oberfläche ein (Backup-Reiter): Cron je Ziel, „Behalten (Anzahl)" und seit
neuestem „Mindestens aufbewahren (Tage)" — Letzteres ist ein Boden, der
verhindert, dass sieben Läufe an einem Nachmittag die Historie auf Stunden
eindampfen.

**Der Schlüssel ist das Backup.** Ohne `PARKRR_BACKUP_KEY` ist jedes Archiv
wertlos verschlüsselter Datenmüll. Den Schlüssel NICHT nur auf dem Server
ablegen, der gesichert wird.

**Prüfen:** jedes Volume-Backup wird nach dem Schreiben automatisch rückgelesen
und probeentpackt (pg_restore --list); nur geprüfte Archive verdrängen alte.
Trotzdem gilt: einmal im Quartal eine echte Wiederherstellung in eine
Wegwerf-Datenbank fahren — ein Backup, das nie restauriert wurde, ist eine
Hoffnung, kein Backup.

**Wiederherstellen im Browser (optional):** Mit
`PARKRR_BROWSER_RESTORE=true` erscheint im Backup-Reiter der Restore-Bereich.
Die Sicherung zuerst validieren und anschließend die angezeigte, an ihre
SHA-256-Prüfsumme gebundene Bestätigung eingeben. Die Anmeldung des Admins darf
dabei höchstens zehn Minuten alt sein. Parkrr schließt neue Anfragen, wartet auf
laufende Anfragen und Hintergrundjobs, hält alle Replikate über eine gemeinsame
Datenbank-Lease an und stellt atomar wieder her. Danach laufen Migrationen und
Konsistenzprüfungen; sämtliche Sitzungen werden widerrufen.

Währenddessen bleibt `/healthz` erreichbar, `/readyz` liefert 503 und der Browser
zeigt den dauerhaften Restore-Status. Bei einem Prozessabbruch wiederholt keine
andere Instanz blind den destruktiven Schritt: sie übernimmt nur die idempotente
Reparatur und Prüfung. Ein abgebrochener Vorgang endet deshalb sichtbar als
fehlgeschlagen und verlangt eine Kontrolle des Datenstands.

Der Browser nimmt Archive bis 1 GiB an. Der Archivschlüssel wird nur im Speicher
der koordinierenden Instanz gehalten und nie in der Datenbank gespeichert. Die
Funktion ist standardmäßig aus; beim bloßen Update wird sie nicht aktiviert.
Bei mehreren App-Replikaten muss die Variable auf allen Replikaten denselben Wert
haben; eine nicht aktivierte Replik hält den exklusiven Restore sonst sicher an.

**CLI-Fallback / bewusst offline:**

```bash
# Zuerst ALLE Parkrr-App-Instanzen stoppen. Der Befehl verweigert den Restore,
# solange auch nur eine Instanz ihre Datenbank-Lease hält.

# Aus einer Archivdatei (destruktiv — überschreibt die Datenbank, atomar):
parkrr restore /backups/parkrr-2026-09-07-030000.dump.enc

# Aus S3: Archiv im Backup-Reiter herunterladen, dann ebenfalls offline mit
# `parkrr restore <datei>` einspielen.
```

Danach Parkrr wieder starten. Fehlende Migrationen werden vom Restore-Befehl
eingespielt; alle Sitzungen werden verworfen, daher melden sich alle Benutzer neu
an. Neue Sicherungen verwenden ein stückweise authentifiziertes Format und bleiben
dadurch auch bei großen Datenbanken speicherbegrenzt; ältere Sicherungen bleiben
lesbar.

## 3. Updates einspielen

Der Weg ist: `dev` → Pull Request → `main` → Deploy (main ist Produktion).

1. Vor dem Update auf die Backup-Kachel sehen: Grün? Wenn nicht: erst Backup.
2. Neues Image ziehen, Container neu erstellen. Migrationen laufen beim Start
   automatisch und sind per Checksumme gegen nachträgliche Veränderung gesichert
   — eine veränderte, bereits angewandte Migration stoppt den Start absichtlich.
3. Die Anwendung meldet neuen Code den offenen Browsern selbst („Neue Version
   verfügbar → Neu laden").

Rollback: voriges Image-Tag deployen. Migrationen sind vorwärtsgerichtet — wenn
eine neue Version Migrationen mitbrachte, ist der saubere Rückweg die
Wiederherstellung des Backups von vor dem Update (deshalb Schritt 1).

## 4. Die Schalter, die Verhalten ändern (alle Default AUS)

| Schalter | Wirkung, wenn eingeschaltet |
|---|---|
| `PARKRR_REQUIRE_2FA` | Konten ohne TOTP/Passkey erreichen nur noch die 2FA-/Passkey-Einrichtung. Vorher prüfen: haben alle Admins einen zweiten Faktor? |
| `PARKRR_PASSKEY_ONLY` | Passwort-Login abgeschaltet. Verlangt WebAuthn (`PARKRR_WEBAUTHN_RP_ID`) — und mindestens ein Konto MIT registriertem Passkey, sonst sperrt man sich aus. Reihenfolge: Passkeys einrichten → testen → Schalter setzen. |
| `PARKRR_AUTO_INVOICE_CRON` | Automatischer Rechnungslauf (z. B. `0 6 1 * *`). Nutzt denselben Pfad wie der „+ Rechnung"-Knopf; abgerechnete Perioden werden nie doppelt fakturiert. Vorher die Verkäufer-Pflichtangaben (Abrechnung → Einstellungen) vollständig ausfüllen, sonst bricht der Lauf mit klarer Meldung ab. |
| `PARKRR_TIMEZONE` | Geschäftszeitzone (z. B. `Europe/Vienna`). Wichtig, wenn der Container in UTC läuft: zwischen 00:00 und 02:00 Wiener Zeit ist in UTC noch gestern — Zahlungen bekämen das Datum von gestern. |
| `PARKRR_ALERT_EMAIL` | Betriebsalarme (fehlgeschlagene Backups) per Mail. Braucht konfiguriertes SMTP. |

### Steuerbereich: zuerst nur lesen, später aktivieren

Der Steuerbereich ist standardmäßig im **Lesemodus**. Administratoren können
Jahre wechseln, vorhandene Einnahmen, Ausgaben, Anlagen und Belege prüfen,
die unverbindliche Jahreswechsel-Simulation ansehen und CSV, PDF oder ZIP
herunterladen. Der normale Ablauf „Buchung → bezahlt markieren → abholen“
setzt weder den Steuerbereich noch einen Rechnungsprozess voraus.

Schreibaktionen sind auch serverseitig gesperrt: Ausgaben und Vorlagen anlegen,
Ausgaben stornieren, Belege hochladen, Anlagen erfassen, Steuerobjekte und
Einstellungen ändern, Zahlungen steuerlich zuordnen sowie Jahre sperren oder
entsperren. Der Lesemodus ist kein leerer Demodatenbestand: bereits vorhandene
Ausgaben, AfA und Zuordnungen gehen weiterhin in die Vorschau ein.

**Später aktivieren:** `PARKRR_TAX_WRITES_ENABLED=true` in der
App-Konfiguration setzen und die Anwendung neu starten. Bei Docker den
App-Container mit der geänderten Konfiguration neu erstellen; ein bloßes
`docker restart` übernimmt keine geänderten Umgebungsvariablen. Bei mehreren
App-Replikaten denselben Wert auf allen Replikaten setzen. Anschließend ist die
Bearbeitung für Administratoren freigegeben. Es werden dadurch keine Rechnungen,
Ausgaben oder Importe erzeugt und keine vereinbarten Preise neu berechnet.
Eine Freigabe in dieser Dokumentation aktiviert keine laufende Instanz.

**Wieder nur lesen:** auf `false` zurückstellen und genauso neu starten bzw.
neu erstellen. Daten werden dabei weder gelöscht noch zurückgesetzt.
Bereits abgeschlossene Steuerjahre bleiben gesperrt und schützen weiterhin
auch normale Zahlungskorrekturen. Zum Entsperren ist die bewusste Aktivierung
der Steuer-Schreibfunktionen nötig; der Lesemodus umgeht diesen Schutz nicht.

Die Aktivierung ist keine steuerliche Abnahme. E1b bleibt eine Vorschau,
KU-Voraussetzungen und maßgebliche Jahres-/Vorjahresumsätze müssen fachlich
geprüft werden. Eine Importvorschau für externe AfA-/Jahreskorrekturen ist vorhanden;
die dauerhafte Übernahme und ein vollständiges jahresbezogenes E1b-Mapping fehlen.
Eine vorhandene einfache AfA-Berechnung ersetzt diese Prüfung nicht.

### Optionale Jahresprüfung ohne Speicherung

Unter „Jahresprüfung vorbereiten · Vorschau“ lassen sich für 2025 und 2026
gesondert ermittelte Jahres-/Vorjahresumsätze eingeben. Leere Angaben bleiben
unbekannt; Zahlungseingänge werden nur zum Vergleich angezeigt, nicht als
maßgeblicher Umsatz übernommen. Die Vorschau ist keine Bestätigung der
KU-Berechtigung. Quelle und Prüferangabe sind deine Eingaben, keine von Parkrr
verifizierten Nachweise.

„Einzelnen Jahreswechsel prüfen“ benötigt tatsächliche Zahlung, vertragliche
Fälligkeit, wirtschaftliches Bezugsjahr und die Angabe, ob der Vorgang regelmäßig
wiederkehrt. Einnahmen und Ausgaben werden unterstützt. Das hinterlegte
15-Tage-Prüffenster stützt sich ergänzend auf eine archivierte BMF-Auslegung;
es ersetzt keine aktuelle Einzelfallprüfung. Kein Ergebnis verschiebt Beträge
in den Berichten. Der JSON-Download ist ein lokaler Prüfentwurf, kein Importformat
für bestätigte Jahreskorrekturen. Beim Verlassen der Ansicht gehen Eingaben
verloren, sofern du keinen Entwurf heruntergeladen hast.

### Externe Jahreswerte als Datei prüfen

„Externe Jahreswerte prüfen · Importvorschau“ ist eine getrennte, standardmäßig
eingeklappte Ansicht für Administratoren. Sie funktioniert auch im Lesemodus und
bei abgeschlossenen Jahren, weil sie keine Steuerdaten liest oder schreibt.
Wähle ein vorhandenes Steuerobjekt und eine UTF-8-JSON-Datei (maximal 64 KiB,
1 bis 100 Einträge). Bei genau einem Objekt ist es vorausgewählt. Erst „Datei
prüfen“ sendet den Inhalt an den eigenen Parkrr-Server, nicht an einen externen
Dienst. Jahr und Objekt der geprüften Datei müssen zur Ansicht passen.

Die Beispieldatei enthält erfundene Werte und Herkunftsangaben; vor einer echten
Prüfung vollständig ersetzen. Dateiformat Version 1: `year` (2025 oder 2026),
`property_id`, `source`, `reviewer` und `entries`. Jeder Eintrag enthält eine
eindeutige `external_id` (1–64 Zeichen: Buchstaben/Ziffern, danach auch `._:-`),
`kind`, `amount_eur` als Dezimaltext und `description`. Herkunft, Prüferangabe
und Beschreibung sind einzeilig, nicht leer und jeweils höchstens 1000 UTF-8-Bytes.

- `income_adjustment`: positive oder negative Einnahmenkorrektur.
- `expense_adjustment`: positive oder negative Werbungskostenkorrektur.
- `external_depreciation`: nicht negativer externer AfA-Wert, **kein Zusatz** zur
  bereits in Parkrr erfassten AfA.

Beträge benötigen einen Dezimalpunkt und höchstens zwei Nachkommastellen,
keine Tausendertrennung oder Exponentialnotation; zum Beispiel `"-20.00"`.
Einzelbeträge und endgültige Gruppensummen sind auf jeweils eine Milliarde EUR
im Betrag begrenzt. Doppelte JSON-Felder, unbekannte Felder, doppelte externe IDs
innerhalb der Datei und ungenaue Beträge werden abgewiesen. Die Vorschau zeigt
drei getrennte Summen, keinen neuen Jahresüberschuss. Bestehende Zahlungen,
Werbungskosten, AfA, E1b und ZIP-Exporte bleiben unverändert.

Quelle und Prüferangabe sind nicht verifiziert. Die API prüft keine Existenz der
Objekt-ID; die Oberfläche gleicht sie mit dem gewählten vorhandenen Objekt ab.
Es gibt keinen Abgleich gegen Bestandsdaten oder früher geprüfte Dateien.
Wiederholtes Prüfen erzeugt keinen Eintrag. „Vorschau herunterladen“ sichert einen
Prüfnachweis im Format `parkrr-tax-import-preview`, kein FinanzOnline- oder
Übernahmeformat. Zum erneuten Prüfen die ursprüngliche Eingabedatei verwenden.
Eine neue Datei, eine andere Objektwahl oder „Auswahl verwerfen“ entfernt das
bisherige Ergebnis; nach einem Fehler bleibt die Auswahl für einen Retry erhalten.
Beim Verlassen der Ansicht wird nichts lokal gespeichert.

### Schutz bestehender Preise und Periodenzahlungen

Neue normale Buchungen übernehmen den aktuellen Tarif. Bestehende Buchungen
behalten ihren vereinbarten Betrag, auch bei künftigem Mietbeginn. Bezieht eine
Altbuchung ihren Preis noch dynamisch aus dem Tarif, wird dessen betroffener
Preis nicht geändert (409 mit Anzahl der abhängigen Buchungen). Bestätige solche
Preise nach Prüfung einzeln oder lege einen neuen Tarif an. Es gibt keinen
automatischen Preis-Backfill. „Erneut einstellen“ kopiert weiterhin bewusst den
alten vereinbarten Preis, einschließlich Sonder- und Nullpreisen.

Vorläufiger Historien-Schutz: Betrag ersetzen, Bezahlt zurücksetzen oder die
Pauschale/Nebenkostenquelle löschen wird mit 409 abgewiesen, sobald dadurch
vorhandene Periodenzahlungen verloren gingen. Das gilt auch für stornierte
Historie. Betrag, Zahlungsdatum, Zuordnungen und Quelle bleiben unverändert.
Neue einzelne Perioden können weiter bezahlt werden; eine identische aktive
Periodenmarkierung erzeugt keinen zweiten Eingang.

Der Pauschalen-Gesamtschalter setzt intern alle Perioden neu auf und ist deshalb
bei vorhandenen Zahlungen ebenfalls gesperrt. Der Nebenkosten-Gesamtschalter darf
neue Perioden ergänzen, aber keine vorhandenen Teilbeträge ersetzen. Für echte
Korrekturen fehlt noch der geprüfte Perioden-Storno-/Neubuchungsablauf. Nicht als
Umgehung den Beleg löschen oder eine zusätzliche Zahlung erfassen. Ein bloßes
Zurücksetzen des Bezahlt-Häkchens wäre ohnehin kein tatsächlicher Geldrückfluss.

### Steuer-ZIP und Ausrollprüfung

Das ZIP liest alle Daten einschließlich Belegen aus demselben lesenden
Datenbank-Snapshot. Nach dessen Materialisierung wird die Verbindung freigegeben;
ein langsamer Download hält sie nicht offen. Fehlende Altverknüpfungen werden
weiterhin als Lücken ausgewiesen, nicht rekonstruiert.

Für Belege gilt standardmäßig ein Gesamtlimit von 24 MiB unkomprimierter Daten
je Export: `PARKRR_TAX_EXPORT_MAX_RECEIPT_MB` (1–1024). Sie liegen während des
Downloads in einer privaten temporären Datei. Im gehärteten Container sind
32 MiB `/tmp` vorgesehen. Vor größeren Limits ein ausreichend großes privates
Volume über `TMPDIR` bereitstellen; parallele Exporte benötigen entsprechend
mehr Platz. Zu wenig Speicher beendet den Export ohne Finanzänderung.

Diese Ergänzungen enthalten keine SQL-Migration und keine automatische
Bestandskorrektur. Vor dem Ausrollen trotzdem Zielversion und bereits
ausstehende ältere Migrationen prüfen, Finanzsummen/Legacy-Preise abgleichen,
ein verschlüsseltes Backup erstellen und genau dieses isoliert wiederherstellen.
Die synthetischen Restore-Tests ersetzen keine Prüfung deines tatsächlichen
Backups. Erst Dev-Abnahme, dann gesonderte Produktionsfreigabe.

Ein Rückfall auf ältere App-Versionen kann die neuen Schutzregeln wieder
entfernen. Dabei keine neuen Zahlungen löschen und keine ältere Datenbank über
zwischenzeitliche Vorgänge zurückspielen. Die spätere R03-Indexänderung braucht
einen eigenen Übergangs-/Rollbackplan: alte Backfill-Abfragen dürfen nicht
ungeprüft gegen einen geänderten Unique-Index laufen.

## 5. Kundenportal

Ein Portal-Link ist ein GEHEIMNIS mit Ablaufdatum (Person → „Portal-Link"):
wer ihn hat, sieht Saldo, Rechnungen (mit Zahlungs-QR), Gefährte und
Übergabeprotokolle dieser Person — ohne Anmeldung. Deshalb:

- Links nur über den vorgesehenen Versand (oder persönlich) weitergeben, nie in
  öffentliche Kanäle.
- Kompromittiert? Person → Portal-Links → widerrufen. Wirkt sofort.
- Das Portal kann nichts ändern: Kundenwünsche (Kontaktdaten, Abholtermin)
  landen als Anfrage auf dem Dashboard und werden dort übernommen — oder nicht.
- Abgelaufene Links räumen sich selbst auf (stündlicher Sweep).

## 6. Nachweise und Aufbewahrung

- **Rechnungen sind unveränderlich** (Datenbank-Trigger, BAO §131/§132):
  Korrektur = Storno + neue Rechnung. Das Storno-Dokument nennt die stornierte
  Nummer.
- **Übergabeprotokolle sind unveränderlich**, sobald unterschrieben.
- **DSGVO-Löschung** (Person → anonymisieren): leert die Personenzeile,
  widerruft Portal-Links und entfernt Name+Unterschrift aus den
  Übergabeprotokollen — die Belege selbst bleiben (aufbewahrungspflichtig).
  Konten ausscheidender MITARBEITER dagegen: **sperren statt löschen**
  (Benutzerverwaltung → „Zugang sperren"), sonst verlieren Rechnungen und
  Zahlungen ihre Urheber-Zuordnung.
- **Revisionsexport** (Audit-Log): JSONL, jede Zeile durch eine SHA-256-Kette an
  alle vorigen gebunden. Das Manifest am Ende (Zeilenzahl + Endglied) getrennt
  ablegen; jeder spätere Export lässt sich dagegen prüfen — mit nichts als
  SHA-256, ohne Parkrr.

## 7. Überwachung (optional)

`/metrics` liefert Prometheus-Metriken (per `PARKRR_METRICS_TOKEN` schützen!).
Fertige Startpunkte liegen im Repo: `ops/prometheus-alerts.yml` (Erreichbarkeit,
5xx-Quote, p95-Latenz, DB-Pool, Neustart-Schleife) und
`ops/grafana-dashboard.json` (importierbar). Die Schwellwerte sind bewusst
unempfindlich — nachschärfen gehört zum Betrieb.

## 8. Wenn etwas klemmt

| Symptom | Erster Griff |
|---|---|
| Anwendung startet nicht | Container-Log: `slog fatal`-Zeile lesen. Häufig: fehlende Pflicht-Variable, unbekannte `PARKRR_TIMEZONE`, veränderte Migration (§3). |
| Alles langsam | Grafana/`parkrr_db_pool_*`: wartet die Anwendung auf DB-Verbindungen? Dann Langläufer in `pg_stat_activity` suchen, nicht den Pool vergrößern. |
| Viele 429 im Log | Der Per-IP-Limiter (`PARKRR_RATE_LIMIT_PER_MIN`, Default 600). Hinter einem Reverse-Proxy `PARKRR_TRUSTED_PROXY` + CIDRs setzen, sonst zählen alle Nutzer als eine IP. |
| Login klemmt trotz richtigem Passwort | Konto gesperrt? (Benutzerverwaltung). Lockout nach Fehlversuchen? 10 Minuten warten. `disabled`-Konten bekommen absichtlich dieselbe Meldung wie falsche Zugangsdaten. |
| Backup-Kachel rot | Änderungsprotokoll → Aktion „Backup fehlgeschlagen": dort steht die Stufe (Dump, Prüfung, Upload) samt Fehler. |
| Mail kommt nicht an | Audit-Log → „E-Mail-Versand": jeder Versuch mit SMTP-Diagnose. |

## 9. Demo & Spielwiese

`parkrr seed-demo` befüllt eine FRISCHE Datenbank mit erkennbaren Demo-Daten
(Präfix „Demo:", idempotent). Auf einer Datenbank mit echten Personen verweigert
das Kommando den Dienst — absichtlich und ohne `--force`.
