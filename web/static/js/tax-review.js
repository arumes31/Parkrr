// A read-only workbench: drafts and results never enter the operational ledger.
export function createTaxReview(rep, { el, field, api, eur }) {
    const section = el('details', { class: 'card tax-details tax-review' },
        el('summary', {}, 'Jahresprüfung vorbereiten · Vorschau'));
    section.append(el('p', { class: 'card-meta' },
        'Optionale Berechnung, auch im Lesemodus. Keine Speicherung, keine Buchung und keine steuerliche Freigabe. Eingaben bleiben nur bis zum Verlassen der Ansicht erhalten.'));
    if (![2025, 2026].includes(rep.year)) {
        section.append(el('p', {}, 'Für dieses Jahr ist kein Regelstand für die Vorschau hinterlegt. Verfügbar: 2025 und 2026.'));
        return section;
    }

    const text = () => el('input', { type: 'text', maxlength: 500, required: true, autocomplete: 'off' });
    const money = () => el('input', { type: 'text', inputmode: 'decimal', maxlength: 16,
        pattern: '[0-9]+([.,][0-9]{1,2})?', placeholder: 'Unbekannt', autocomplete: 'off' });
    const select = (options) => el('select', {}, ...options.map(([value, label]) => el('option', { value }, label)));
    const amount = control => control.value.trim().replace(',', '.') || undefined;

    // Each draft has its own result. Editing invalidates a pending or old result.
    function wire(form, buildRequest, kind) {
        const result = el('div', { class: 'tax-review-result', role: 'status', 'aria-live': 'polite' });
        const error = el('p', { role: 'alert', hidden: true });
        const submit = el('button', { type: 'submit', class: 'btn btn-primary btn-sm' }, 'Vorschau berechnen');
        const download = el('button', { type: 'button', class: 'btn btn-ghost btn-sm', disabled: true }, 'Prüfentwurf herunterladen');
        let revision = 0, saved = null;
        form.addEventListener('input', () => {
            revision++;
            saved = null;
            download.disabled = true;
            result.replaceChildren();
            error.hidden = true;
        });
        form.addEventListener('submit', async event => {
            event.preventDefault();
            if (submit.disabled || !form.reportValidity()) return;
            const currentRevision = revision;
            const request = buildRequest();
            error.hidden = true;
            saved = null;
            download.disabled = true;
            submit.disabled = true;
            result.textContent = 'Vorschau wird berechnet …';
            try {
                const response = await api.post('/reports/tax-review/preview', request);
                if (currentRevision !== revision || !form.isConnected) return;
                if (response.preview !== true || response.persisted !== false || !response[kind]) {
                    throw new Error('Keine gültige Vorschau erhalten. Bitte erneut versuchen.');
                }
                const data = response[kind];
                const labels = {
                    unknown: 'Angaben fehlen – keine Aussage möglich',
                    within_limit: 'Angegebene Umsätze innerhalb der Grenzen',
                    within_tolerance: 'Angegebener Jahresumsatz im Toleranzbereich',
                    current_limit_exceeded: 'Angegebener Jahresumsatz über der Toleranzgrenze',
                    previous_limit_exceeded: 'Angegebener Vorjahresumsatz über der Grenze',
                    opted_out: 'Verzicht auf die Befreiung angegeben',
                    candidate: 'Mögliche Jahresverschiebung – fachlich prüfen',
                    same_year: 'Zahlung und wirtschaftliche Zuordnung im selben Jahr',
                    outside_window: 'Datumsangaben außerhalb des Prüffensters',
                    not_recurring: 'Nicht als regelmäßig wiederkehrend angegeben',
                };
                result.replaceChildren(el('h4', {}, labels[data.status] || 'Prüfergebnis'),
                    el('p', {}, 'Nur Vorschau · nicht gespeichert · nicht fachlich bestätigt'));
                if (data.suggested_year) result.append(el('p', {}, 'Vorgeschlagenes Jahr: ' + data.suggested_year));
                if (Array.isArray(data.notes)) {
                    result.append(el('ul', {}, ...data.notes.map(note => el('li', {}, note))));
                }
                saved = { format: 'parkrr-tax-review-draft', version: 1, input: request, result: response };
                download.disabled = false;
            } catch (err) {
                if (currentRevision !== revision || !form.isConnected) return;
                result.replaceChildren();
                error.textContent = err.message + ' Deine Eingaben bleiben erhalten.';
                error.hidden = false;
            } finally {
                submit.disabled = false;
            }
        });
        download.addEventListener('click', () => {
            if (!saved) return;
            const url = URL.createObjectURL(new Blob([JSON.stringify(saved, null, 2)], { type: 'application/json' }));
            const link = el('a', { href: url, download: 'parkrr-pruefentwurf-' + rep.year + '-' + kind + '.json' });
            document.body.append(link);
            link.click();
            link.remove();
            setTimeout(() => URL.revokeObjectURL(url), 4000);
        });
        form.append(el('div', { class: 'btn-row tax-review-wide' }, submit, download), error, result);
    }

    const current = money(), previous = money(), source = text(), reviewer = text();
    const opted = select([['', 'Unbekannt'], ['false', 'Kein Verzicht angegeben'], ['true', 'Auf Befreiung verzichtet']]);
    const turnover = el('form', { class: 'tax-review-grid', 'aria-label': 'Jahresumsätze vergleichen' },
        field('Maßgeblicher Umsatz ' + rep.year + ' (EUR)', current, 'Brutto-Leistungsumsatz; leer bedeutet unbekannt, nicht null.'),
        field('Maßgeblicher Umsatz ' + (rep.year - 1) + ' (EUR)', previous, 'Vorjahreswert aus einer gesonderten Umsatzermittlung.'),
        field('Umsatzsteuer-Option', opted), field('Quelle der Umsatzwerte', source), field('Prüferangabe zu den Umsatzwerten', reviewer));
    section.append(el('h4', {}, 'Jahresumsätze vergleichen'),
        el('p', { class: 'card-meta' }, 'Zum Vergleich: ' + eur(rep.total_by_date) + ' erfasste Zahlungseingänge. Dieser Wert wird nicht als maßgeblicher Umsatz übernommen.'), turnover);
    wire(turnover, () => ({ year: rep.year, turnover: {
        current_eur: amount(current), previous_eur: amount(previous), receipts_eur: Number(rep.total_by_date).toFixed(2),
        opted_out: opted.value === '' ? undefined : opted.value === 'true', source: source.value.trim(), reviewer: reviewer.value.trim(),
    } }), 'turnover');

    // Uncommon contractual checks stay collapsed; no date is guessed from a payment.
    const shift = el('details', { class: 'tax-details' }, el('summary', {}, 'Einzelnen Jahreswechsel prüfen'));
    const kind = select([['income', 'Einnahme'], ['expense', 'Ausgabe']]);
    const economicYear = el('input', { type: 'number', min: 2025, max: 2026, value: rep.year, required: true });
    const paid = el('input', { type: 'date', required: true }), due = el('input', { type: 'date', required: true });
    const regular = select([['', 'Unbekannt'], ['true', 'Ja'], ['false', 'Nein']]);
    const dueSource = text(), dueReviewer = text();
    const shiftForm = el('form', { class: 'tax-review-grid', 'aria-label': 'Jahreswechsel prüfen' },
        field('Vorgangsart', kind), field('Wirtschaftliches Bezugsjahr', economicYear),
        field('Tatsächliches Zahlungsdatum', paid), field('Vertragliche Fälligkeit', due),
        field('Regelmäßig wiederkehrend', regular), field('Quelle der Fälligkeit', dueSource), field('Prüferangabe zur Fälligkeit', dueReviewer));
    shift.append(el('p', { class: 'card-meta' }, 'Einzelfall-Vorschau für Einnahmen oder Ausgaben. Quelle und Prüferangabe dokumentieren deine Eingabe; Parkrr bestätigt weder den Vertrag noch eine steuerliche Zuordnung.'), shiftForm);
    wire(shiftForm, () => ({ year: rep.year, year_shift: {
        kind: kind.value, economic_year: Number(economicYear.value), paid_on: paid.value, due_on: due.value,
        regularly_recurring: regular.value === '' ? undefined : regular.value === 'true', source: dueSource.value.trim(), reviewer: dueReviewer.value.trim(),
    } }), 'year_shift');
    section.append(shift);
    return section;
}
