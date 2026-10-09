const kinds = {
    income_adjustment: 'Einnahmenkorrekturen',
    expense_adjustment: 'Werbungskostenkorrekturen',
    external_depreciation: 'Externe AfA',
};
const maxBytes = 64 * 1024;

// External values remain a separate draft: never add them to the tax ledger.
export function createTaxImport(rep, { el, field, api, eur }) {
    const section = el('details', { class: 'card tax-details tax-import' },
        el('summary', {}, 'Externe Jahreswerte prüfen · Importvorschau'));
    section.append(el('p', { class: 'card-meta' },
        'Jahreskorrekturen und externe AfA als JSON-Datei prüfen. Nur Vorschau: keine Speicherung, keine Buchung und keine Änderung der Jahressummen.'));
    if (![2025, 2026].includes(rep.year)) {
        section.append(el('p', {}, 'Das Dateiformat unterstützt derzeit die Jahre 2025 und 2026.'));
        return section;
    }
    const properties = (rep.properties || []).filter(p => Number.isSafeInteger(p.id) && p.id > 0);
    if (!properties.length) {
        section.append(el('p', {}, 'Noch kein Steuerobjekt vorhanden. Die Importvorschau benötigt eine eindeutige Objektzuordnung. Im Lesemodus wird kein Objekt angelegt.'));
        return section;
    }
    const property = el('select', { required: true },
        el('option', { value: '' }, 'Steuerobjekt wählen'),
        ...properties.map(p => el('option', { value: p.id }, p.name)));
    // Only a single known object is a safe default; multiple objects need a choice.
    if (properties.length === 1) property.value = String(properties[0].id);
    const file = el('input', { type: 'file', accept: '.json,application/json', required: true });
    const submit = el('button', { class: 'btn btn-primary btn-sm', type: 'submit' }, 'Datei prüfen');
    const example = el('button', { class: 'btn btn-ghost btn-sm', type: 'button' }, 'Beispieldatei herunterladen');
    const download = el('button', { class: 'btn btn-ghost btn-sm', type: 'button', disabled: true }, 'Vorschau herunterladen');
    const reset = el('button', { class: 'btn btn-ghost btn-sm', type: 'button' }, 'Auswahl verwerfen');
    const error = el('p', { role: 'alert', hidden: true });
    const status = el('p', { role: 'status', 'aria-live': 'polite', class: 'tax-import-status' });
    const result = el('div', { class: 'tax-import-result' });
    const form = el('form', { class: 'tax-review-grid', 'aria-label': 'Externe Jahreswerte prüfen' },
        field('Steuerobjekt für die Vorschau', property, 'Jahr ' + rep.year + ' und Objekt müssen mit der Datei übereinstimmen.'),
        field('JSON-Datei mit Jahreswerten', file, 'UTF-8 · maximal 64 KiB · 1 bis 100 Einträge. Erst „Datei prüfen“ sendet den Inhalt an deinen Parkrr-Server.'),
        el('div', { class: 'btn-row tax-review-wide' }, submit, example, download, reset), error);
    section.append(form, status, result);

    const help = el('details', { class: 'tax-details tax-import-help' }, el('summary', {}, 'Dateiformat und Grenzen'));
    help.append(el('p', {}, 'Die Beispieldatei enthält ausschließlich erfundene Werte. Quelle und Prüferangabe sind selbst angegebene Herkunftshinweise, keine Bestätigung durch Parkrr.'),
        el('ul', {},
            el('li', {}, 'version: 1, year, property_id, source und reviewer sind Pflichtfelder.'),
            el('li', {}, 'Jeder Eintrag benötigt external_id, kind, amount_eur und description. IDs müssen innerhalb der Datei eindeutig sein.'),
            el('li', {}, 'Beträge als Text, zum Beispiel "120.50" oder "-20.00". Keine Tausendertrennzeichen und höchstens zwei Nachkommastellen.'),
            el('li', {}, 'income_adjustment und expense_adjustment sind Korrekturbeträge mit Vorzeichen: positiv erhöht, negativ vermindert die jeweilige Kategorie. external_depreciation ist ein nicht negativer externer AfA-Betrag.'),
            el('li', {}, 'Externe AfA wird nicht zu vorhandener AfA addiert oder mit ihr verrechnet. Kein Abgleich mit Bestandsdaten, kein vollständiges E1b-Mapping und kein FinanzOnline-Import.')));
    section.append(help);

    let revision = 0, busy = false, saved = null;
    const invalidate = () => {
        revision++;
        saved = null;
        download.disabled = true;
        result.replaceChildren();
        status.textContent = '';
        error.hidden = true;
    };
    const syncExample = () => { example.disabled = !property.value; };
    property.addEventListener('change', () => { invalidate(); syncExample(); });
    file.addEventListener('change', invalidate);
    syncExample();

    function downloadJSON(data, name) {
        const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }));
        const link = el('a', { href: url, download: name });
        document.body.append(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(url), 4000);
    }
    example.addEventListener('click', () => {
        if (!property.value) return;
        downloadJSON({ version: 1, year: rep.year, property_id: Number(property.value),
            source: 'SYNTHETISCHES BEISPIEL – durch echte Herkunft ersetzen', reviewer: 'Beispielperson – ersetzen',
            entries: [
                { external_id: 'beispiel-einnahme-1', kind: 'income_adjustment', amount_eur: '-20.00', description: 'Erfundene Einnahmenkorrektur' },
                { external_id: 'beispiel-kosten-1', kind: 'expense_adjustment', amount_eur: '25.50', description: 'Erfundene Werbungskostenkorrektur' },
                { external_id: 'beispiel-afa-1', kind: 'external_depreciation', amount_eur: '100.00', description: 'Erfundener externer AfA-Wert – nicht zusätzlich buchen' },
            ] }, 'parkrr-jahreswerte-beispiel-' + rep.year + '.json');
    });
    reset.addEventListener('click', () => {
        invalidate();
        file.value = '';
        file.focus();
    });
    download.addEventListener('click', () => {
        if (saved) downloadJSON(saved, 'parkrr-importvorschau-' + rep.year + '.json');
    });
    form.addEventListener('submit', async event => {
        event.preventDefault();
        if (busy || !form.reportValidity()) return;
        invalidate();
        const currentRevision = revision;
        const selectedProperty = Number(property.value);
        const selectedFile = file.files[0];
        const isCurrent = () => revision === currentRevision && form.isConnected;
        busy = true;
        submit.disabled = true;
        form.setAttribute('aria-busy', 'true');
        status.textContent = 'Datei wird geprüft …';
        try {
            if (!selectedFile || selectedFile.size === 0 || selectedFile.size > maxBytes) {
                throw new Error('Bitte eine nicht leere JSON-Datei mit höchstens 64 KiB auswählen.');
            }
            let jsonText;
            try {
                // Fatal decoding prevents silently replacing invalid bytes; preserve duplicate keys for the server.
                jsonText = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(await selectedFile.arrayBuffer());
            } catch {
                throw new Error('Die Datei ist nicht als UTF-8 lesbar. Bitte als UTF-8-JSON speichern und erneut auswählen.');
            }
            if (!isCurrent()) return;
            const data = await api.postJSONText('/reports/tax-import/preview', jsonText);
            if (!isCurrent()) return;
            if (data.preview !== true || data.persisted !== false || data.verified !== false || data.version !== 1) {
                throw new Error('Keine gültige Importvorschau erhalten. Bitte erneut versuchen.');
            }
            if (data.year !== rep.year || data.property_id !== selectedProperty) {
                throw new Error('Jahr oder Steuerobjekt der Datei passt nicht zur Auswahl. Bitte Datei oder Auswahl korrigieren.');
            }
            const validEntries = Array.isArray(data.entries) && data.entries.length > 0 && data.entries.length <= 100 &&
                data.entries.every(entry => Object.hasOwn(kinds, entry.kind) && Number.isSafeInteger(entry.amount_cents));
            const validTotals = data.totals_cents && Object.keys(kinds).every(kind => Number.isSafeInteger(data.totals_cents[kind]));
            if (!validEntries || !validTotals) throw new Error('Unvollständige Importvorschau erhalten. Bitte erneut versuchen.');

            const objectName = properties.find(p => p.id === selectedProperty).name;
            result.append(el('h4', {}, 'Externe Werte · ' + rep.year + ' · ' + objectName),
                el('p', {}, 'Quelle: ' + data.source), el('p', {}, 'Prüferangabe: ' + data.reviewer));
            const totals = el('dl', { class: 'tax-import-totals' });
            for (const [kind, label] of Object.entries(kinds)) {
                totals.append(el('div', {}, el('dt', {}, label), el('dd', {}, eur(data.totals_cents[kind] / 100))));
            }
            result.append(totals, el('p', { class: 'card-meta' },
                'Diese Summen gehören nur zur Datei. Bestehende Einnahmen, Werbungskosten, AfA und E1b-Werte bleiben unverändert. Doppelerfassung mit Bestandsdaten fachlich prüfen.'));
            const entries = el('details', { class: 'tax-details' }, el('summary', {}, 'Einträge ansehen · ' + data.entries.length));
            entries.append(el('ul', { class: 'tax-import-entries' }, ...data.entries.map(entry => el('li', {},
                el('div', {}, el('b', {}, entry.description), el('small', {}, kinds[entry.kind] + ' · ' + entry.external_id)),
                el('span', { class: 'tax-import-amount' }, eur(entry.amount_cents / 100))))));
            result.append(entries);
            if (Array.isArray(data.notes)) result.append(el('ul', {}, ...data.notes.map(note => el('li', {}, note))));
            saved = { format: 'parkrr-tax-import-preview', version: 1, result: data };
            download.disabled = false;
            status.textContent = data.entries.length + ' Einträge geprüft · nicht gespeichert · nicht fachlich bestätigt.';
        } catch (err) {
            if (!isCurrent()) return;
            status.textContent = '';
            result.replaceChildren();
            error.textContent = err.message + ' Die Auswahl bleibt erhalten; keine Daten wurden übernommen.';
            error.hidden = false;
        } finally {
            busy = false;
            submit.disabled = false;
            form.removeAttribute('aria-busy');
        }
    });
    return section;
}
