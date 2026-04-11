var charts = {};
var PCOLS = ['#00cc8f','#00b4ff','#e54949','#fbbf24','#a855f7','#f5a623','#ec4899'];

function esc(s) {
  var d = document.createElement('div');
  d.appendChild(document.createTextNode(String(s)));
  return d.innerHTML;
}

function uDonut(id, labels, data, colors) {
  var el = document.getElementById(id);
  if (!el) return;
  if (charts[id]) {
    charts[id].data.labels = labels;
    charts[id].data.datasets[0].data = data;
    charts[id].data.datasets[0].backgroundColor = colors;
    charts[id].update('none');
  } else {
    charts[id] = new Chart(el, {
      type: 'doughnut',
      data: { labels: labels, datasets: [{ data: data, backgroundColor: colors, borderWidth: 0, hoverOffset: 4 }] },
      options: {
        cutout: '76%',
        plugins: { legend: { display: false } },
        maintainAspectRatio: false
      }
    });
  }
}

function uLine(id, hData) {
  var el = document.getElementById(id);
  if (!el || !hData || hData.length === 0) return;
  if (charts[id]) {
    charts[id].data.labels = hData.map(function(p){ return p.time; });
    charts[id].data.datasets[0].data = hData.map(function(p){ return p.reqCost; });
    charts[id].data.datasets[1].data = hData.map(function(p){ return p.useCost; });
    charts[id].update('none');
  } else {
    charts[id] = new Chart(el, {
      type: 'line',
      data: {
        labels: hData.map(function(p){ return p.time; }),
        datasets: [
          { label: 'Budget ($)', borderColor: '#e54949', borderWidth: 1.5,
            data: hData.map(function(p){ return p.reqCost; }),
            pointRadius: 0, tension: 0.3, fill: false },
          { label: 'Actual ($)', borderColor: '#00cc8f', borderWidth: 1.5,
            data: hData.map(function(p){ return p.useCost; }),
            fill: true, backgroundColor: 'rgba(0,204,143,.06)', pointRadius: 0, tension: 0.3 }
        ]
      },
      options: {
        responsive: true, maintainAspectRatio: false,
        interaction: { mode: 'index', intersect: false },
        plugins: {
          legend: { display: false },
          tooltip: { backgroundColor: '#1a1e27', borderColor: '#2d3347', borderWidth: 1,
                     titleColor: '#c8d0e0', bodyColor: '#7a8499' }
        },
        scales: {
          y: { grid: { color: 'rgba(45,51,71,.55)' },
               ticks: { 
                 color: '#7a8499', 
                 font: { family: 'JetBrains Mono', size: 10 },
                 callback: function(value) {
                   return '$' + value.toFixed(6);
                 }
               } 
          },
          x: { grid: { display: false },
               ticks: { color: '#7a8499', maxTicksLimit: 8, font: { size: 10 } } }
        }
      }
    });
  }
}

async function update() {
  try {
    var s = await (await fetch('/api/summary')).json();
    var nodes    = s.nodes || [];
    var byPhase  = s.podsByPhase || {};
    var failed   = s.failedPods || [];
    var pending  = s.pendingPods || [];
    var eff      = s.efficiency || 0;
    var running  = byPhase['Running'] || 0;
    var total    = Object.values(byPhase).reduce(function(a,b){ return a+b; }, 0);
    var issues   = nodes.filter(function(n){ return n.status !== 'Running'; }).length;

    document.getElementById('kN').textContent   = nodes.length;
    document.getElementById('kNs').textContent  = issues > 0 ? issues + ' with issues' : 'All healthy';
    document.getElementById('kR').textContent   = running;
    document.getElementById('kRs').textContent  = 'of ' + total + ' total';
    document.getElementById('kF').textContent   = failed.length;
    document.getElementById('kFs').textContent  = pending.length + ' pending';
    document.getElementById('kE').textContent   = eff.toFixed(1) + '%';
    document.getElementById('kEs').textContent  = s.cpuRequested + 'm / ' + s.cpuAllocatable + 'm';
    document.getElementById('effBig').textContent = eff.toFixed(1) + '%';
    document.getElementById('cpuReqV').textContent = s.cpuRequested + 'm';
    document.getElementById('cpuAlcV').textContent = s.cpuAllocatable + 'm';

    var reqPct = s.cpuAllocatable > 0 ? (s.cpuRequested / s.cpuAllocatable * 100) : 0;
    var rb = document.getElementById('cpuReqB');
    rb.style.width = Math.min(reqPct, 100) + '%';
    rb.style.background = reqPct > 85 ? 'var(--red)' : reqPct > 70 ? 'var(--orange)' : 'var(--cyan)';

    var cpuBadge = document.getElementById('cpubadge');
    cpuBadge.textContent = eff > 85 ? 'Critical' : eff > 70 ? 'High Load' : 'Optimal';
    cpuBadge.className = 'badge ' + (eff > 85 ? 'b-crit' : eff > 70 ? 'b-warn' : 'b-ok');

    uDonut('cpuDonut',
      ['Requested','Free'],
      [s.cpuRequested, Math.max(0, s.cpuAllocatable - s.cpuRequested)],
      ['#00b4ff','#2d3347']
    );

    var hc = document.getElementById('honeycomb');
    hc.innerHTML = '';
    nodes.forEach(function(n) {
      var d = document.createElement('div');
      d.className = 'hex ' + (n.status === 'Running' ? 'ok' : 'issue');
      d.title = n.name;
      d.textContent = 'N';
      hc.appendChild(d);
    });
    var nb = document.getElementById('nbadge');
    nb.textContent = issues > 0 ? issues + ' Issues' : 'All OK';
    nb.className = 'badge ' + (issues > 0 ? 'b-crit' : 'b-ok');

    var phases = Object.keys(byPhase);
    var phaseVals = Object.values(byPhase);
    uDonut('phaseDonut', phases, phaseVals, PCOLS.slice(0, phases.length));
    var leg = '';
    phases.forEach(function(ph, i) {
      leg += '<div class="li"><div class="li-dot" style="background:' + PCOLS[i] + '"></div>' +
             '<span class="li-lbl">' + esc(ph) + '</span>' +
             '<span class="li-val">' + phaseVals[i] + '</span></div>';
    });
    document.getElementById('phaseLegend').innerHTML = leg;

    var ahtml = '';
    failed.forEach(function(p) {
      ahtml += '<div class="alert failed"><span class="alert-ico" style="color:var(--red)">&#9888;</span>' +
               '<div><b>' + esc(p.name) + '</b><div class="alert-ns">' + esc(p.namespace) + ' &bull; FAILED</div></div></div>';
    });
    pending.forEach(function(p) {
      ahtml += '<div class="alert pending"><span class="alert-ico" style="color:var(--orange)">&#9203;</span>' +
               '<div><b>' + esc(p.name) + '</b><div class="alert-ns">' + esc(p.namespace) + ' &bull; PENDING</div></div></div>';
    });
    document.getElementById('alertsBox').innerHTML = ahtml ||
      '<div class="alert ok"><span style="color:var(--green)">&#10003;</span>&nbsp; No active alerts &mdash; cluster healthy</div>';
    var totalA = failed.length + pending.length;
    var ab = document.getElementById('abadge');
    ab.textContent = totalA > 0 ? totalA + ' Issues' : '0 Issues';
    ab.className = 'badge ' + (failed.length > 0 ? 'b-crit' : totalA > 0 ? 'b-warn' : 'b-ok');

    var m = await (await fetch('/api/metrics')).json();
    m = m || [];
    if (m.length > 0) {
      document.getElementById('kT').textContent  = m[0].cpuUsage + 'm';
      document.getElementById('kTs').textContent = m[0].name || '--';
    }
    var maxCpu = m.length > 0 ? m[0].cpuUsage : 1;
    var rows = '';
    m.slice(0, 10).forEach(function(p, i) {
      var pct = maxCpu > 0 ? (p.cpuUsage / maxCpu * 100) : 0;
      var fc = pct > 80 ? 'var(--red)' : pct > 55 ? 'var(--orange)' : 'var(--cyan)';
      var cpuRequestText = p.cpuRequestPresent ? (p.cpuRequest + 'm') : 'N/A';
      var hasSaving = Number(p.potentialSavingMCpu || 0) > 0;
      var oppLabel = hasSaving ? ('-' + Number(p.potentialSavingMCpu) + 'm') : '';
      var opp = hasSaving
        ? '<span style="color:var(--orange);font-family:monospace">' + esc(oppLabel) + ' CPU</span>'
        : '<span style="color:var(--green)">&#10003;</span>';
      rows += '<tr>' +
        '<td style="color:var(--text-dim)">' + (i+1) + '</td>' +
        '<td class="mono" style="max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' + esc(p.name||'--') + '</td>' +
        '<td><span class="ns-tag">' + esc(p.namespace||'--') + '</span></td>' +
        '<td class="mono" style="color:var(--cyan)">' + p.cpuUsage + 'm</td>' +
        '<td class="mono" style="color:var(--text-dim)">' + esc(cpuRequestText) + '</td>' +
        '<td><div class="util-wrap"><div class="util-bg"><div class="util-fill" style="width:' + pct.toFixed(0) + '%;background:' + fc + '"></div></div>' +
            '<span class="util-pct">' + pct.toFixed(0) + '%</span></div></td>' +
        '<td>' + opp + '</td>' +
        '</tr>';
    });
    document.getElementById('wbody').innerHTML = rows ||
      '<tr><td colspan="7" style="text-align:center;color:var(--text-dim);padding:16px">No workload data</td></tr>';

    var waste = m.filter(function(p){ return Number(p.potentialSavingMCpu || 0) > 0; });
    document.getElementById('kW').textContent = waste.length;
    var wc = document.getElementById('wcnt');
    wc.textContent = waste.length + ' item' + (waste.length !== 1 ? 's' : '');
    wc.className = 'badge ' + (waste.length > 0 ? 'b-warn' : 'b-ok');
    if (waste.length > 0) {
      document.getElementById('wasteList').innerHTML = waste.slice(0, 5).map(function(p) {
        return '<div class="waste-item"><div class="waste-name">' + esc(p.name) + '</div>' +
               '<div class="waste-row"><span style="color:var(--text-dim)">Savings opportunity</span>' +
               '<span class="waste-save">-' + esc(Number(p.potentialSavingMCpu)) + 'm CPU</span></div>' +
               '<div class="waste-bar"><div class="waste-fill" style="width:65%"></div></div></div>';
      }).join('');
    } else {
      document.getElementById('wasteList').innerHTML =
        '<div class="alert ok"><span style="color:var(--green)">&#10003;</span>&nbsp; All workloads rightsized</div>';
    }

    var h = await (await fetch('/api/history')).json();
    uLine('mainLineChart', h);

    document.getElementById('lastUp').textContent = 'Updated: ' + new Date().toLocaleTimeString();
  } catch(e) { console.error('Sentinel update error:', e); }
}
setInterval(update, 5000);
update();
