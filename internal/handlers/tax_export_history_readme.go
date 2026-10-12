package handlers

const taxPaymentHistoryReadme = `
Zahlungsverlauf (Exportformat 3)
------------------------------
zahlungsverlauf.csv enthält auch korrigierte Originalzahlungen. Beträge sind
ursprünglich gespeicherte Zahlbeträge, keine zusätzlichen Einnahmen und keine
negativen Rückzahlungen. Nicht zu einnahmen.csv addieren. in_einnahmen_csv
verknüpft den Verlauf mit der unveränderten Einnahmentabelle; diese enthält
auch die Kandidaten der optionalen Jahreswechsel-Simulation (regeljahr beachten).

Auswahl: Buchungs-/aktuelles Zuflussjahr, Kandidaten am Jahreswechsel,
Rücknahmejahr und Zahlungs-/Zuordnungsänderungen im ausgewählten Jahr.
Frühere Zuflussjahre aus erhaltenen Metadatenänderungen werden berücksichtigt,
auch wenn das Datum inzwischen nochmals geändert wurde. Der Verlauf zeigt den
aktuellen Exportstand, keinen rekonstruierten Stand zum damaligen Jahresende.

zahlungsaudit.csv enthält die vorhandenen Zahlungs- und Steuerzuordnungsereignisse
sowie über erhaltene Zahlungslinks zuordenbare Rechnungsstornos. Bei payment und
payment_tax_metadata ist entitaet_id die payment_id; bei invoice die invoice_id.
aenderungen_json enthält die gespeicherten Vorher-/Nachherwerte. Die Listen
released_allocations und released_invoice_payments sichern entfernte Beziehungen
mit ihren IDs, payment_id, Referenzen und Beträgen. Ein Rechnungsstorno gibt
Guthaben frei; eine Zahlungskorrektur ist keine tatsächliche Auszahlung.

Fehlende historische Details werden nicht erfunden: zuordnungsnachweis_fehlt
kennzeichnet korrigierte Zahlungen ohne erhaltenen Zuordnungssnapshot.
zuordnungsnachweis_vorhanden bestätigt nur einen vorhandenen Snapshot, keine
lückenlose Gesamthistorie. Früher gelöschte Periodenzahlungen oder Zuordnungen
können fehlen; das betrifft insbesondere ältere manuelle Rücknahmen und Stornos.
Echte Rückzahlungen und Kautionen sind weiterhin kein eigenständiges Modell.

Alle Daten des Pakets einschließlich Einnahmen, Zuordnungen, Verlauf, Audit,
Ausgaben, AfA, Steuerzusammenfassung und Belegen stammen aus demselben lesenden
Datenbank-Snapshot. Währenddessen abgeschlossene Änderungen erscheinen erst im
nächsten Export. Vor PDF-/ZIP-Erstellung und Download wird die Datenbankverbindung
freigegeben. Belege werden dafür vorübergehend in einer privaten Datei abgelegt;
diese wird auch bei Fehlern oder Abbruch entfernt. Das Belegvolumen ist zum Schutz
des temporären Speichers standardmäßig auf 24 MiB pro Paket begrenzt. Für größere
Pakete kann die Administration PARKRR_TAX_EXPORT_MAX_RECEIPT_MB (1–1024 MiB)
zusammen mit einem entsprechend dimensionierten, geschützten TMPDIR konfigurieren.
Das gehärtete Standard-Deployment hat nur 32 MiB /tmp; gleichzeitige Exporte
teilen diesen Speicher. Limitüberschreitung oder Speicherfehler brechen vor dem
Download ab, ohne gespeicherte Daten zu verändern.
Objekt-/Personenbezeichnungen sind aktuelle Labels des Snapshots.
Stabile IDs sind für Verknüpfungen maßgeblich. Auditbenutzer sind historische
Protokollangaben, keine zusätzlichen Kundendaten oder Zugangsinformationen.
`
