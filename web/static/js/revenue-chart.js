const months = ['Jänner', 'Februar', 'März', 'April', 'Mai', 'Juni', 'Juli', 'August', 'September', 'Oktober', 'November', 'Dezember'];
const shortMonths = ['J', 'F', 'M', 'A', 'M', 'J', 'J', 'A', 'S', 'O', 'N', 'D'];
const sumMoney = values => values.reduce((total, value) => total + Math.round(value * 100), 0) / 100;
const validMonths = values => Array.isArray(values) && values.length === 12 && values.every(Number.isFinite);

// A separate read-only view: forecasts never replace overview amounts or payments.
export function createRevenueChart(overview, { el, eur, api, chartLine }) {
    const currentYear = Number.isInteger(overview.current_year) ? overview.current_year : new Date().getFullYear();
    let mode = 'month', targetYear = overview.year, data = null, busy = false, failure = '', ticket = 0;
    const cache = new Map();
    const card = el('section', { class: 'chart-card revenue-chart', 'aria-label': 'Umsatzverlauf und optionale Prognose' });
    const title = el('h3', {}, 'Umsatz pro Monat · ' + targetYear);
    const monthButton = el('button', { class: 'btn btn-ghost btn-sm', type: 'button', 'aria-pressed': 'true' }, 'Monate');
    const yearButton = el('button', { class: 'btn btn-ghost btn-sm', type: 'button', 'aria-pressed': 'false' }, 'Jahre');
    const forecast = el('input', { type: 'checkbox', disabled: overview.year < currentYear });
    const horizon = el('select', { 'aria-label': 'Planungsjahr der Umsatzprognose' },
        el('option', { value: currentYear }, currentYear + ' · Restjahr'),
        el('option', { value: currentYear + 1 }, currentYear + 1 + ' · Planung'));
    horizon.value = String(currentYear);
    const horizonLabel = el('label', { class: 'revenue-horizon', hidden: true }, 'Planungsjahr ', horizon);
    const compare = el('input', { type: 'checkbox' });
    const reservations = el('input', { type: 'checkbox', disabled: true });
    const detailsSummary = el('summary', {}, 'Werte & Vergleich');
    const details = el('details', { class: 'revenue-details' }, detailsSummary);
    const evidence = el('div', { class: 'revenue-evidence' });
    const values = el('div', { class: 'revenue-value-details' });
    details.append(el('label', { class: 'revenue-option' }, compare, 'Vorjahr als Monatslinie'),
        el('label', { class: 'revenue-option' }, reservations, 'Reservierungen zusätzlich berücksichtigen'),
        el('p', { class: 'card-meta' }, 'Reservierungen sind ein separates Szenario, keine gesicherten Einnahmen. Die Prognose zeigt auflaufende Miete und Zusatzkosten, nicht Zahlungseingänge oder Steuerwerte.'), evidence, values);
    const message = el('div', { class: 'revenue-message', role: 'status', 'aria-live': 'polite' });
    const error = el('div', { class: 'load-notice', role: 'alert', hidden: true });
    const content = el('div', { class: 'revenue-content' });
    const controls = el('div', { class: 'revenue-controls' },
        el('div', { class: 'revenue-modes', role: 'group', 'aria-label': 'Darstellung des Umsatzes' }, monthButton, yearButton),
        el('label', { class: 'revenue-option' }, forecast, 'Prognose anzeigen'), horizonLabel);
    card.append(title, controls);
    if (overview.year < currentYear) card.append(el('p', { class: 'card-meta' }, 'Vergangenes Jahr: keine rückwirkende Prognose. Für eine Planung das aktuelle Jahr wählen.'));
    card.append(message, error, content, details);

    function validate(response, year) {
        if (!response || response.year !== year || response.current_year !== currentYear ||
            !/^\d{4}-\d{2}-\d{2}$/.test(response.as_of || '') ||
            !validMonths(response.actual_by_month) || !validMonths(response.forecast_by_month) || !validMonths(response.reservations_by_month) ||
            !(response.previous_by_month === null || validMonths(response.previous_by_month)) ||
            !Array.isArray(response.assumptions) || !response.assumptions.every(item => typeof item === 'string') ||
            !Array.isArray(response.warnings) || !response.warnings.every(item => typeof item === 'string') ||
            !Array.isArray(response.years) || !response.years.length || response.years.length > 102 ||
            !response.years.every(row => Number.isInteger(row.year) && [row.actual, row.forecast, row.reservations].every(Number.isFinite))) {
            throw new Error('Die gelieferten Umsatzwerte sind unvollständig.');
        }
        return response;
    }

    function needsData() { return forecast.checked || compare.checked || mode === 'year'; }

    async function update() {
        const activeTicket = ++ticket;
        targetYear = forecast.checked ? Number(horizon.value) : overview.year;
        horizonLabel.hidden = !forecast.checked;
        reservations.disabled = !forecast.checked;
        compare.disabled = mode === 'year';
        const cached = cache.get(targetYear);
        data = cached && Date.now() - cached.loadedAt < 30000 ? cached.value : null;
        failure = '';
        busy = needsData() && !data;
        render();
        if (!busy) return;
        try {
            const loaded = validate(await api.get('/overview/forecast?year=' + targetYear), targetYear);
            if (activeTicket !== ticket || !card.isConnected) return;
            cache.set(targetYear, { value: loaded, loadedAt: Date.now() });
            data = loaded;
        } catch (err) {
            if (activeTicket !== ticket || !card.isConnected) return;
            failure = err.message || 'Die Umsatzvorschau konnte nicht geladen werden.';
        } finally {
            if (activeTicket === ticket && card.isConnected) {
                busy = false;
                render();
            }
        }
    }

    monthButton.addEventListener('click', () => { mode = 'month'; update(); });
    yearButton.addEventListener('click', () => { mode = 'year'; update(); });
    forecast.addEventListener('change', () => { if (!forecast.checked) reservations.checked = false; update(); });
    horizon.addEventListener('change', update);
    compare.addEventListener('change', update);
    reservations.addEventListener('change', render);

    function render() {
        const showForecast = forecast.checked && !!data;
        const includeReservations = showForecast && reservations.checked;
        title.textContent = (mode === 'month' ? 'Umsatz pro Monat · ' : 'Umsatz im Jahresvergleich · ') + targetYear;
        monthButton.setAttribute('aria-pressed', String(mode === 'month'));
        yearButton.setAttribute('aria-pressed', String(mode === 'year'));
        content.setAttribute('aria-busy', String(busy));
        message.textContent = busy ? 'Umsatzwerte werden geladen … Bestehende Beträge bleiben unverändert.' : '';
        error.hidden = !failure;
        error.replaceChildren();
        if (failure) {
            error.append(el('span', {}, failure + ' Es werden keine Ersatzwerte geschätzt.'),
                el('button', { class: 'btn btn-ghost btn-sm', type: 'button', onclick: update }, 'Erneut versuchen'));
        }
        evidence.replaceChildren();
        values.replaceChildren();
        detailsSummary.textContent = 'Werte & Vergleich' + (data?.warnings?.length
            ? ' · ' + data.warnings.length + (data.warnings.length === 1 ? ' Hinweis' : ' Hinweise') : '');
        content.replaceChildren();
        // Preserve the familiar actual-only chart until optional data is available.
        if (!data) {
            if (mode === 'month' && targetYear === overview.year) {
                content.append(chartLine(overview.revenue_by_month, shortMonths, 'Umsatz pro Monat'),
                    el('p', { class: 'card-meta' }, 'Aufgelaufen · Miete + Zusatzkosten. Keine Zahlungseingänge.'));
            } else {
                content.append(el('p', { class: 'card-meta' }, busy ? 'Die Zeitreihe wird vorbereitet.' : 'Für diese Ansicht liegen noch keine geprüften Werte vor.'));
            }
            return;
        }
        evidence.append(el('p', { class: 'card-meta' }, 'Datenstand: ' + data.as_of + '. Keine neuen Buchungen, Preisänderungen oder automatische Übernahme.'),
            el('ul', {}, ...(data.assumptions || []).map(text => el('li', {}, text))));
        if (data.warnings?.length) evidence.append(el('p', {}, 'Hinweise zur Datenbasis'), el('ul', {}, ...data.warnings.map(text => el('li', {}, text))));
        if (compare.checked && mode === 'month' && data.previous_by_month === null) {
            content.append(el('p', { class: 'revenue-note' }, 'Keine Vergleichsdaten für ' + (targetYear - 1) + ' – das ist kein Nullumsatz.'));
        }

        let rows;
        if (mode === 'month') {
            const cutoff = Number(data.as_of.slice(5, 7)) - 1;
            rows = months.map((name, index) => ({
                name,
                label: shortMonths[index],
                actual: targetYear < currentYear || (targetYear === currentYear && index <= cutoff) ? data.actual_by_month[index] : null,
                forecast: showForecast ? data.forecast_by_month[index] : null,
                reservations: includeReservations ? data.reservations_by_month[index] : null,
                previous: compare.checked && data.previous_by_month ? data.previous_by_month[index] : null,
                projected: showForecast && (targetYear > currentYear || (targetYear === currentYear && index >= cutoff)),
            }));
        } else {
            rows = data.years.filter(row => row.year <= Math.min(targetYear, showForecast ? currentYear + 1 : currentYear))
                .map(row => ({ name: String(row.year), label: String(row.year), actual: row.year <= currentYear ? row.actual : null,
                    forecast: showForecast && row.year >= currentYear ? row.forecast : null,
                    reservations: includeReservations && row.year >= currentYear ? row.reservations : null,
                    previous: null, projected: showForecast && row.year >= currentYear }));
        }
        if (!rows.length) {
            content.append(el('p', {}, 'Für diesen Zeitraum sind keine Jahreswerte vorhanden.'));
            return;
        }
        const columns = [{ key: 'actual', label: 'Aufgelaufen' }];
        if (showForecast) columns.push({ key: 'forecast', label: 'Bestand · Restprognose' });
        if (includeReservations) columns.push({ key: 'reservations', label: 'Reservierungen · Szenario' });
        if (showForecast) columns.push({ key: 'expected', label: includeReservations ? 'Gesamt mit Szenario' : 'Voraussichtlich gesamt' });
        if (mode === 'month' && compare.checked) columns.push({ key: 'previous', label: 'Vorjahr ' + (targetYear - 1) });
        rows.forEach(row => { row.expected = showForecast ? sumMoney([row.actual || 0, row.forecast || 0, row.reservations || 0]) : null; });
        const plotted = mode === 'year' ? rows.slice(-5) : rows;
        if (mode === 'year' && plotted.length < rows.length) content.append(el('p', { class: 'card-meta' }, 'Diagramm: letzte fünf Datenjahre. Alle gelieferten Jahre stehen in der Wertetabelle.'));
        content.append(makePlot(plotted, columns, { annual: mode === 'year', showForecast, includeReservations }));
        const legend = el('div', { class: 'revenue-legend' }, legendItem('actual', 'Aufgelaufen'));
        if (showForecast) legend.append(legendItem('forecast', 'Bestand · Prognose'));
        if (includeReservations) legend.append(legendItem('reservation', 'Mit Reservierungen'));
        if (mode === 'month' && compare.checked && data.previous_by_month) legend.append(legendItem('previous', String(targetYear - 1) + ' · Vorjahr'));
        content.append(legend);
        if (showForecast) {
            const actual = sumMoney(data.actual_by_month), remaining = sumMoney(data.forecast_by_month), reserved = includeReservations ? sumMoney(data.reservations_by_month) : 0;
            const totals = el('dl', { class: 'revenue-totals', 'aria-label': 'Jahresvorschau ' + targetYear });
            for (const [label, value] of [['Aufgelaufen ' + targetYear, actual], ['Bestand · Restprognose', remaining],
                ...(includeReservations ? [['Reservierungen · Szenario', reserved]] : []),
                [includeReservations ? 'Gesamt mit Szenario' : 'Voraussichtlich gesamt', sumMoney([actual, remaining, reserved])]]) {
                totals.append(el('div', {}, el('dt', {}, label), el('dd', {}, eur(value))));
            }
            content.append(totals);
            evidence.append(el('p', { class: 'card-meta' }, 'Monatsprognose = aufgelaufener Betrag + verbleibender Zeitraum. Ohne Garantie neuer Buchungen oder tatsächlicher Zahlungen.'));
        }
        const tableDetails = el('details', { class: 'revenue-values' }, el('summary', {}, 'Werte als Tabelle'));
        const table = el('table', {}, el('caption', { class: 'sr-only' }, title.textContent),
            el('thead', {}, el('tr', {}, el('th', { scope: 'col' }, mode === 'month' ? 'Monat' : 'Jahr'), ...columns.map(column => el('th', { scope: 'col' }, column.label)))),
            el('tbody', {}, ...rows.map(row => el('tr', {}, el('th', { scope: 'row' }, row.name), ...columns.map(column => el('td', {}, moneyOrMissing(row[column.key])))))));
        tableDetails.append(el('p', { class: 'card-meta' }, '— bedeutet: noch kein Ist-Wert oder keine Vergleichsdaten; 0,00 € ist ein vorhandener Nullwert.'),
            el('div', { class: 'revenue-table-scroll', tabindex: '0', role: 'region', 'aria-label': 'Umsatzwerte, horizontal scrollbar' }, table));
        values.append(tableDetails);
    }

    function moneyOrMissing(value) { return value == null ? '—' : eur(value); }
    function legendItem(kind, text) { return el('span', {}, el('i', { class: 'revenue-key revenue-key-' + kind, 'aria-hidden': 'true' }), text); }

    function makePlot(rows, columns, { annual, showForecast, includeReservations }) {
        const width = 360, height = 184, left = annual ? 24 : 12, right = annual ? 24 : 12, top = 16, bottom = 24;
        const base = rows.map(row => row.actual);
        const projected = rows.map(row => row.projected ? sumMoney([row.actual || 0, row.forecast || 0]) : null);
        const reserved = rows.map(row => row.projected ? row.expected : null);
        const previous = rows.map(row => row.previous);
        const values = [...base, ...(showForecast ? projected : []), ...(includeReservations ? reserved : []), ...previous].filter(value => value != null);
        const low = Math.min(0, ...values), high = Math.max(1, ...values), range = high - low;
        const x = index => left + (rows.length === 1 ? (width - left - right) / 2 : index * (width - left - right) / (rows.length - 1));
        const y = value => top + (height - top - bottom) * (1 - (value - low) / (range * 1.1));
        const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('viewBox', '0 0 ' + width + ' ' + height);
        svg.setAttribute('role', 'img');
        svg.setAttribute('aria-label', title.textContent + (showForecast ? '. Durchgezogen: aufgelaufen. Gestrichelt: Prognose.' : '. Aufgelaufene Beträge.'));
        const node = (tag, attrs) => {
            const item = document.createElementNS(svg.namespaceURI, tag);
            Object.entries(attrs).forEach(([key, value]) => item.setAttribute(key, value));
            svg.append(item);
            return item;
        };
        for (let step = 0; step <= 3; step++) node('line', { class: 'revenue-grid', x1: left, x2: width - right,
            y1: top + step * (height - top - bottom) / 3, y2: top + step * (height - top - bottom) / 3 });
        if (annual) {
            rows.forEach((row, index) => {
                const barWidth = Math.min(32, (width - left - right) / rows.length * 0.5), center = x(index);
                if (row.actual != null) node('rect', { class: 'revenue-bar-actual', x: center - barWidth / 2, y: y(Math.max(0, row.actual)),
                    width: barWidth, height: Math.abs(y(row.actual) - y(0)) });
                if (showForecast && row.projected) {
                    const from = row.actual || 0, to = sumMoney([from, row.forecast || 0]);
                    node('rect', { class: 'revenue-bar-forecast', x: center - barWidth / 2, y: Math.min(y(from), y(to)), width: barWidth,
                        height: Math.abs(y(to) - y(from)) });
                    if (includeReservations) node('rect', { class: 'revenue-bar-reservation', x: center - barWidth / 2,
                        y: Math.min(y(to), y(row.expected)), width: barWidth, height: Math.abs(y(row.expected) - y(to)) });
                }
            });
        } else {
            for (const [series, cls] of [[previous, 'previous'], [base, 'actual'], ...(showForecast ? [[projected, 'forecast']] : []),
                ...(includeReservations ? [[reserved, 'reservation']] : [])]) {
                let drawing = false, points = '';
                series.forEach((value, index) => {
                    if (value == null) { drawing = false; return; }
                    points += (drawing ? ' L ' : ' M ') + x(index) + ' ' + y(value);
                    drawing = true;
                    node('circle', { class: 'revenue-point revenue-series-' + cls, cx: x(index), cy: y(value), r: 2.4 });
                });
                if (points) node('path', { class: 'revenue-line revenue-series-' + cls, d: points });
            }
        }
        rows.forEach((row, index) => { node('text', { class: 'revenue-axis', x: x(index), y: height - 5, 'text-anchor': 'middle' }).textContent = row.label; });
        const focus = node('line', { class: 'revenue-focus', x1: left, x2: left, y1: top, y2: height - bottom, visibility: 'hidden' });
        const wrapper = el('div', { class: 'revenue-plot', tabindex: '0', role: 'group',
            'aria-label': title.textContent + '. Pfeiltasten wechseln den Zeitraum; Pos1 und Ende springen an die Grenzen.' }, svg);
        const readout = el('div', { class: 'revenue-readout', 'aria-hidden': 'true' }, 'Werte: berühren oder Pfeiltasten nutzen.');
        const live = el('span', { class: 'sr-only', 'aria-live': 'polite' });
        wrapper.append(readout, live);
        let index = -1;
        function select(next) {
            index = Math.max(0, Math.min(rows.length - 1, next));
            const row = rows[index];
            focus.setAttribute('x1', x(index)); focus.setAttribute('x2', x(index)); focus.setAttribute('visibility', 'visible');
            readout.replaceChildren(el('strong', {}, row.name), ...columns.map(column => el('span', {}, column.label + ': ' + moneyOrMissing(row[column.key]))));
            live.textContent = row.name + '. ' + columns.map(column => column.label + ': ' + (row[column.key] == null ? 'kein Wert' : eur(row[column.key]))).join('. ');
        }
        const point = event => {
            const matrix = svg.getScreenCTM();
            if (!matrix) return;
            const local = new DOMPoint(event.clientX, event.clientY).matrixTransform(matrix.inverse());
            select(Math.round((local.x - left) / ((width - left - right) / (rows.length - 1 || 1))));
        };
        svg.addEventListener('pointermove', point);
        svg.addEventListener('pointerdown', point);
        wrapper.addEventListener('keydown', event => {
            let next;
            if (event.key === 'ArrowRight') next = index < 0 ? 0 : index + 1;
            else if (event.key === 'ArrowLeft') next = index < 0 ? rows.length - 1 : index - 1;
            else if (event.key === 'Home') next = 0;
            else if (event.key === 'End') next = rows.length - 1;
            else return;
            event.preventDefault(); select(next);
        });
        return wrapper;
    }

    render();
    return card;
}
