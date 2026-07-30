/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        // low-saturation warm-neutral palette (no blue-purple gradients)
        sand: {
          50: '#faf9f7',
          100: '#f4f2ee',
          200: '#e8e4dc',
          300: '#d6d0c4',
          400: '#b8b0a0',
          500: '#968d7a',
          600: '#7a7261',
          700: '#625c4f',
          800: '#504b41',
          900: '#454037',
        },
        clay: {
          50: '#faf5f2',
          100: '#f3e8e1',
          200: '#e4cfc2',
          300: '#d0ac97',
          400: '#bc8a6e',
          500: '#a96f52',
          600: '#8f5a41',
          700: '#764837',
          800: '#613d31',
          900: '#51342b',
        },
        moss: {
          50: '#f6f7f4',
          100: '#e8ece3',
          200: '#d1d9c8',
          300: '#aebda1',
          400: '#879d78',
          500: '#68805a',
          600: '#516546',
          700: '#41503a',
          800: '#374131',
          900: '#2f372b',
        },
      },
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'monospace'],
      },
    },
  },
  plugins: [],
}
