export const exorynTokens = {
  "meta": {
    "name": "EXORYN Product Design System",
    "version": "1.0.0",
    "defaultTheme": "light"
  },
  "color": {
    "brand": {
      "navy": "#071426",
      "blue": "#2563FF",
      "cyan": "#12C7D8",
      "ink": "#111827",
      "slate": "#5B6780",
      "ice": "#EAF2FF",
      "mist": "#F5F8FC",
      "white": "#FFFFFF",
      "success": "#0EA56B",
      "warning": "#F2A93B",
      "danger": "#E5484D"
    },
    "surface": {
      "canvas": "#F5F8FC",
      "panel": "#FFFFFF",
      "panelSubtle": "#F9FBFE",
      "inverse": "#071426",
      "selected": "#EAF2FF"
    },
    "border": {
      "subtle": "#DCE5F0",
      "strong": "#B9C7D8",
      "focus": "#2563FF"
    },
    "text": {
      "primary": "#071426",
      "secondary": "#5B6780",
      "muted": "#738097",
      "inverse": "#FFFFFF",
      "link": "#1B4FD8"
    },
    "semanticText": {
      "success": "#087A4F",
      "warning": "#8A5A00",
      "danger": "#B4232B",
      "info": "#1B4FD8",
      "declared": "#6B3FA0",
      "unknown": "#5B6780"
    },
    "semanticBg": {
      "success": "#E7F7F0",
      "warning": "#FFF5DD",
      "danger": "#FDEBEC",
      "info": "#EAF2FF",
      "verified": "#E7FAFC",
      "declared": "#F2ECFA",
      "unknown": "#EEF2F6"
    },
    "severity": {
      "low": "#1B4FD8",
      "medium": "#8A5A00",
      "high": "#B4232B",
      "critical": "#7D1120"
    }
  },
  "type": {
    "family": {
      "display": "Space Grotesk, Inter, ui-sans-serif, system-ui, sans-serif",
      "body": "Inter, ui-sans-serif, system-ui, sans-serif",
      "mono": "IBM Plex Mono, ui-monospace, SFMono-Regular, Menlo, monospace"
    },
    "size": {
      "displayLg": 40,
      "headingXl": 30,
      "headingLg": 24,
      "headingMd": 18,
      "bodyLg": 16,
      "bodyMd": 14,
      "bodySm": 12,
      "monoSm": 12,
      "metricLg": 30
    }
  },
  "space": {
    "1": 4,
    "2": 8,
    "3": 12,
    "4": 16,
    "6": 24,
    "8": 32,
    "10": 40,
    "12": 48,
    "16": 64
  },
  "radius": {
    "sm": 6,
    "md": 12,
    "lg": 20,
    "pill": 999
  },
  "shadow": {
    "panel": "0 1px 2px rgba(7,20,38,.05), 0 8px 24px rgba(7,20,38,.04)",
    "floating": "0 12px 32px rgba(7,20,38,.14)"
  },
  "layout": {
    "sidebar": 232,
    "sidebarCompact": 72,
    "topbar": 64,
    "pageMax": 1600,
    "panelGap": 16
  },
  "motion": {
    "fast": "120ms",
    "base": "180ms",
    "slow": "220ms"
  },
  "breakpoint": {
    "mobile": 640,
    "tablet": 768,
    "desktop": 1024,
    "wide": 1440
  }
} as const;
