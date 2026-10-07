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

Verlauf, Audit und aktuelle Zahlungslinks stammen aus einem gemeinsamen
lesenden Datenbank-Snapshot. Ein zwischen Einnahmenaufstellung und Verlauf
geänderter Einnahmenbestand (Zahlungs-IDs, Beträge, Buchungs-/Zuflussdaten oder
Steuerobjekt-IDs) führt zum Abbruch mit Aufforderung zur Wiederholung. Änderungen
an Zuordnungen oder Einnahmenkategorien werden damit nicht vollständig erkannt.
Zum Beispiel kann ein paralleler Rechnungsstorno noch zu einer alten Kategorie
in einnahmen.csv bei bereits freigegebenen Links in zuordnungen.csv führen.
Das Gesamtpaket einschließlich Steuerzusammenfassung und Ausgaben ist noch
kein einheitlicher Snapshot; für die endgültige Abstimmung ohne parallele
Bearbeitung exportieren. Objekt-/Personenbezeichnungen sind aktuelle Labels.
Stabile IDs sind für Verknüpfungen maßgeblich. Auditbenutzer sind historische
Protokollangaben, keine zusätzlichen Kundendaten oder Zugangsinformationen.
`
