(function () {
  mermaid.initialize({
    startOnLoad: false,
    theme: 'dark',
    securityLevel: 'loose',
    flowchart: { htmlLabels: true, useMaxWidth: true },
    themeVariables: {
      primaryColor: '#1e293b',
      primaryTextColor: '#e2e8f0',
      primaryBorderColor: '#38bdf8',
      secondaryColor: '#0f172a',
      tertiaryColor: '#334155',
      lineColor: '#64748b',
      textColor: '#e2e8f0',
    }
  });

  function clamp(value, min, max) {
    return Math.min(max, Math.max(min, value));
  }

  // Adds zoom and pan to a rendered diagram.
  function enhance(container) {
    if (container.dataset.zoomReady === 'true') return;
    const svg = container.querySelector('svg');
    if (!svg) return;

    container.dataset.zoomReady = 'true';
    container.classList.add('is-enhanced');

    const viewport = document.createElement('div');
    viewport.className = 'mermaid-viewport';
    svg.parentNode.insertBefore(viewport, svg);
    viewport.appendChild(svg);

    const toolbar = document.createElement('div');
    toolbar.className = 'mermaid-toolbar';
    toolbar.innerHTML = [
      '<button type="button" data-action="zoom-out" title="Zoom out">-</button>',
      '<span class="zoom-level" data-role="level">100%</span>',
      '<button type="button" data-action="zoom-in" title="Zoom in">+</button>',
      '<button type="button" data-action="fit" title="Fit width">Fit</button>',
      '<button type="button" data-action="reset" title="Reset view">Reset</button>'
    ].join('');
    container.insertBefore(toolbar, viewport);

    const state = { scale: 1, x: 0, y: 0, dragging: false, startX: 0, startY: 0, originX: 0, originY: 0 };
    const level = toolbar.querySelector('[data-role="level"]');

    function applyTransform() {
      svg.style.transform = `translate(${state.x}px, ${state.y}px) scale(${state.scale})`;
      level.textContent = `${Math.round(state.scale * 100)}%`;
    }

    function zoomBy(delta) {
      state.scale = clamp(state.scale + delta, 0.25, 3);
      applyTransform();
    }

    function reset() {
      state.scale = 1;
      state.x = 0;
      state.y = 0;
      applyTransform();
    }

    function fitWidth() {
      const rawWidth = svg.viewBox && svg.viewBox.baseVal && svg.viewBox.baseVal.width
        ? svg.viewBox.baseVal.width
        : svg.getBoundingClientRect().width / state.scale;
      if (!rawWidth) {
        reset();
        return;
      }
      state.scale = clamp((viewport.clientWidth - 48) / rawWidth, 0.25, 1.4);
      state.x = 0;
      state.y = 0;
      applyTransform();
    }

    toolbar.addEventListener('click', (event) => {
      const button = event.target.closest('button[data-action]');
      if (!button) return;
      const action = button.dataset.action;
      if (action === 'zoom-in') zoomBy(0.15);
      if (action === 'zoom-out') zoomBy(-0.15);
      if (action === 'fit') fitWidth();
      if (action === 'reset') reset();
    });

    viewport.addEventListener('wheel', (event) => {
      if (!event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      zoomBy(event.deltaY < 0 ? 0.1 : -0.1);
    }, { passive: false });

    viewport.addEventListener('pointerdown', (event) => {
      if (event.button !== 0) return;
      state.dragging = true;
      state.startX = event.clientX;
      state.startY = event.clientY;
      state.originX = state.x;
      state.originY = state.y;
      viewport.classList.add('is-dragging');
      viewport.setPointerCapture(event.pointerId);
    });

    viewport.addEventListener('pointermove', (event) => {
      if (!state.dragging) return;
      state.x = state.originX + event.clientX - state.startX;
      state.y = state.originY + event.clientY - state.startY;
      applyTransform();
    });

    function endDrag(event) {
      if (!state.dragging) return;
      state.dragging = false;
      viewport.classList.remove('is-dragging');
      if (viewport.hasPointerCapture(event.pointerId)) {
        viewport.releasePointerCapture(event.pointerId);
      }
    }

    viewport.addEventListener('pointerup', endDrag);
    viewport.addEventListener('pointercancel', endDrag);
    applyTransform();
  }

  // Renders one diagram, once, the first time it is needed. Until then its
  // source waits in a <template>, where the browser neither shows nor lays
  // it out.
  function render(container) {
    if (!container.classList.contains('mermaid-pending')) return;
    const source = container.querySelector('template');
    container.innerHTML = source ? source.innerHTML : '';
    container.className = 'mermaid';
    Promise.resolve(mermaid.run({ nodes: [container] }))
      .catch((error) => console.error('Mermaid render failed:', error))
      .then(() => enhance(container));
  }

  function start() {
    const diagrams = Array.from(document.querySelectorAll('.mermaid-pending'));
    if (!('IntersectionObserver' in window)) {
      diagrams.forEach(render);
      return;
    }
    // A diagram is laid out only when it comes near the screen, so a page of
    // many sections opens as fast as a page of one.
    const observer = new IntersectionObserver((entries) => {
      entries.forEach((entry) => {
        if (!entry.isIntersecting) return;
        observer.unobserve(entry.target);
        render(entry.target);
      });
    }, { rootMargin: '300px 0px' });
    diagrams.forEach((diagram) => observer.observe(diagram));
    // Printing shows every section at once.
    window.addEventListener('beforeprint', () => diagrams.forEach(render));
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
})();
