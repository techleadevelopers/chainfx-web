// --- State Management and Core Logic (from second block, slightly adapted) ---
const state = {
  action: 'buy',
  payAmount: 0, // Will be updated from input
  payCurrency: 'BRL',
  receiveCurrency: 'USDT',
  selectedPaymentMethod: null, // Will store the DOM element
  exchangeRate: 0, // Will be fetched
  currentStep: 1,
  cardCheckoutStep: 1,
  walletAddress: '', // Will be updated from input
  connected: false, // Simulated wallet connection state
  transactionFee: 0.015,
  totalPayAmount: 0,
  platformFee: 0,
  sellNetwork: 'BSC',
  walletBalance: { // Simulated balances
      USDT: 0,
      ETH: 0,
      BRL: 100000
  },
  transactionHistory: []
};

const LIQUIDITY_POOLS = { // Simulated liquidity
  USDT: { reserve: 100000, price: 5 }, // price will be updated by fetched rate
  ETH: { reserve: 1000, price: 15000 },
  EURUSDT: { reserve: 50000, price: 6 },
  BTC: { reserve: 100, price: 350000 }
};

const CURRENCY_OPTIONS = {
  fiat: [
    { code: 'BRL', label: 'BRL', icon: 'https://res.cloudinary.com/limpeja/image/upload/v1783198241/brl_csztl5.png' }
  ],
  crypto: [
    { code: 'USDT', label: 'USDT', icon: 'https://cdn.jsdelivr.net/gh/atomiclabs/cryptocurrency-icons/32/color/usdt.png' },
    { code: 'EURUSDT', label: 'EURUSDT', icon: 'https://res.cloudinary.com/limpeja/image/upload/v1783198240/eur_r6gcvd.png' },
    { code: 'BTC', label: 'BTC', icon: 'https://cdn.jsdelivr.net/gh/atomiclabs/cryptocurrency-icons/32/color/btc.png' }
  ]
};

const SELL_RECEIVE_OPTIONS = [
  { code: 'BRL', label: 'BRL', icon: 'https://res.cloudinary.com/limpeja/image/upload/v1783198241/brl_csztl5.png' }
];

const SELL_NETWORKS = {
  BSC: {
    code: 'BSC',
    shortLabel: 'BSC',
    displayLabel: 'Rede BSC / BEP20',
    icon: 'https://cryptologos.cc/logos/bnb-bnb-logo.png?v=033'
  },
  POLYGON: {
    code: 'POLYGON',
    shortLabel: 'POL',
    displayLabel: 'Rede Polygon / POL',
    icon: 'https://cryptologos.cc/logos/polygon-matic-logo.png?v=033'
  }
};

const FIAT_RATES_TO_BRL = {
  BRL: 1,
  USD: 0,
  EUR: 0
};

const priceState = {
  source: '',
  fetchedAt: 0,
  sellWallet: '0x7e3BF3FDfeF16040CE3ec60A663381766d3dB375',
  sellNetwork: 'BEP20',
  sellNetworks: ['BSC'],
  rates: {
    USDTBRL: 0,
    SELLUSDTBRL: 0,
    USD: 0,
    EUR: 0,
    EURBRL: 0,
    BTCUSDT: 0,
    BTCBRL: 0
  }
};

const steps = { // Step definitions
  1: 'Valor', // Amount in Portuguese
  2: 'Carteira + Dados', // Wallet + payer data
  3: 'Método de Pagamento', // Payment Method
  4: 'Aguardando PIX',
  5: 'Pagamento confirmado'
};

// Helper functions (mostly from second block)

const validateWalletAddress = (address) => {
  // O payment-gateway entrega USDT na BSC/BEP20.
  if (!address) return false;
  const a = address.trim();
  return /^0x[a-fA-F0-9]{40}$/.test(a);
};

const calculateFees = (amount) => amount * state.transactionFee;

const connectWallet = async () => {
  // Simulate wallet connection
  await new Promise(resolve => setTimeout(resolve, 500)); // Simulate async operation
  state.connected = true;
  // Simula um endereco BSC valido para nao travar a validacao.
  state.walletAddress = '0x' + Array.from({ length: 40 }, () => Math.floor(Math.random() * 16).toString(16)).join('');
  return state.walletAddress;
};

const UX_MESSAGES = {
  invalid_amount: 'Valor invalido.',
  invalid_wallet: 'Wallet BSC invalida.',
  payment_method_required: 'Selecione um metodo de pagamento.',
  payer_name_required: 'Informe o nome do pagador.',
  payer_cpf_required: 'Informe o CPF do pagador.',
  payer_cpf_invalid: 'CPF do pagador invalido.',
  card_fields_required: 'Preencha os dados do cartao e endereco.',
  card_token_unavailable: 'Tokenizacao Efi indisponivel.',
  backend_unavailable: 'Servico indisponivel.',
  quote_unavailable: 'Cotacao indisponivel.',
  order_limit: 'Tente novamente em instantes.',
  order_value_limit: 'Valor fora do limite.',
  asset_unsupported: 'Ativo indisponivel.',
  duplicate_payment: 'Pagamento ja processado.',
  webhook_pending: 'Aguardando confirmacao.',
  payment_identified: 'Pagamento identificado.',
  payment_sent: 'USDT enviado.',
  payment_failed: 'Falha no pagamento.',
  signer_missing: 'Envio indisponivel.',
  pix_missing: 'PIX indisponivel.',
  network_error: 'Falha de conexao.',
  unknown: 'Nao foi possivel continuar.'
};

const normalizeUxMessage = (input, fallback = 'unknown') => {
  const raw = String(input || '').toLowerCase();
  if (!raw) return UX_MESSAGES[fallback] || UX_MESSAGES.unknown;
  if (raw.includes('limite diario') || raw.includes('limite diário')) return input;
  if (raw.includes('limite') || raw.includes('too many') || raw.includes('429')) return UX_MESSAGES.order_limit;
  if (raw.includes('fora dos limites') || raw.includes('valor insuficiente')) return UX_MESSAGES.order_value_limit;
  if (raw.includes('asset') || raw.includes('ativo')) return UX_MESSAGES.asset_unsupported;
  if (raw.includes('invalid_customer_document') || raw.includes('cpf')) return UX_MESSAGES.payer_cpf_invalid;
  if (raw.includes('duplicate') || raw.includes('duplic')) return UX_MESSAGES.duplicate_payment;
  if (raw.includes('pagseguro') || raw.includes('pagbank') || raw.includes('pix')) return UX_MESSAGES.pix_missing;
  if (raw.includes('signer') || raw.includes('hmac')) return UX_MESSAGES.signer_missing;
  if (raw.includes('fetch') || raw.includes('network') || raw.includes('backend') || raw.includes('failed')) return UX_MESSAGES.backend_unavailable;
  return input.length <= 72 ? input : (UX_MESSAGES[fallback] || UX_MESSAGES.unknown);
};

const ensureUxMessageHost = () => {
  let host = document.getElementById('uxMessageHost');
  const card = document.querySelector('.swap-card');
  if (!host) {
    host = document.createElement('div');
    host.id = 'uxMessageHost';
    host.className = 'ux-message-host';
  }
  if (card && host.parentElement !== card) {
    card.appendChild(host);
  } else if (!host.parentElement) {
    document.body.appendChild(host);
  }
  return host;
};

const showUxMessage = (message, type = 'info', opts = {}) => {
  const text = UX_MESSAGES[message] || normalizeUxMessage(message, opts.fallback || 'unknown');
  if (opts.inlineEl) {
    opts.inlineEl.textContent = text;
    opts.inlineEl.className = `ux-inline-message ${type}`;
    opts.inlineEl.style.display = text ? 'block' : 'none';
  }
  const host = ensureUxMessageHost();
  const item = document.createElement('div');
  item.className = `ux-message ${type}`;
  item.setAttribute('role', type === 'error' ? 'alert' : 'status');
  const label = document.createElement('span');
  label.textContent = text;
  const closeButton = document.createElement('button');
  closeButton.type = 'button';
  closeButton.setAttribute('aria-label', 'Fechar mensagem');
  closeButton.textContent = 'x';
  item.append(label, closeButton);
  const close = () => {
    item.classList.remove('show');
    setTimeout(() => item.remove(), 180);
  };
  closeButton.addEventListener('click', close);
  host.appendChild(item);
  requestAnimationFrame(() => item.classList.add('show'));
  setTimeout(close, opts.duration || 3600);
  return text;
};

// We will replace the internal getExchangeRate with a fetch call directly in DOMContentLoaded

const executeTransaction = async () => {
  // Simulate transaction execution
  const amountBrl = state.payAmount;
  const amountCrypto = parseFloat(document.getElementById('receiveAmount').value); // Use the calculated received amount
  const fee = calculateFees(amountBrl);
  const totalBrl = amountBrl + fee; // Assuming buying, fee is added to BRL cost

  const pool = LIQUIDITY_POOLS[state.receiveCurrency];

  if (!pool || pool.reserve < amountCrypto) {
      throw new Error('Liquidez insuficiente de Bitcoin.'); // Portuguese
  }

  // Simulate slippage (simplified)
  const slippage = amountBrl > 10000 ? 0.01 : 0.005;
  const finalPrice = state.exchangeRate * (1 + slippage); // Apply slippage to the rate

  if (state.action === 'buy') {
      if (state.walletBalance[state.payCurrency] < totalBrl) {
          throw new Error('Saldo BRL insuficiente na carteira simulada.'); // Portuguese
      }

      state.walletBalance[state.payCurrency] -= totalBrl;
      state.walletBalance[state.receiveCurrency] += amountCrypto;
      pool.reserve -= amountCrypto;
  }
  // Sell logic would be different but is not used in this flow

  const receipt = {
      txHash: '0x' + Math.random().toString(16).slice(2), // Simulate transaction hash
      amountPaid: amountBrl,
      amountReceived: amountCrypto,
      fee: fee,
      totalPaid: totalBrl,
      price: finalPrice, // Price with slippage
      timestamp: Date.now(),
      status: 'completed'
  };

  state.transactionHistory.push(receipt);
  return receipt;
};

// Payment Modal/Processing Functions (from second block)

const generatePixCode = async (paymentDetails) => {
   // Simulate async PIX code generation
   await new Promise(resolve => setTimeout(resolve, 500));
   return `PIX-CODE-${paymentDetails.id}-${Math.random().toString(36).substr(2, 9).toUpperCase()}`;
};

const showPixPaymentModal = (paymentDetails) => {
  // Basic modal creation - requires CSS for styling
  const modal = document.createElement('div');
  modal.className = 'payment-modal pix-modal'; // Add a class for specific styling
  modal.innerHTML = `
      <div class="payment-modal-content">
          <h3>Pagamento via PIX</h3>
          <div class="qr-code-placeholder" style="text-align:center; padding: 20px; border: 1px solid #ccc;">${paymentDetails.pixCode}</div>
          <p>Escaneie ou copie o código PIX.</p>
          <p>Valor: ${paymentDetails.amount} ${paymentDetails.currency}</p>
          <p>ID do Pagamento: ${paymentDetails.id}</p>
          <div class="payment-status" style="margin-top: 15px; font-weight: bold;">Aguardando pagamento...</div>
           <button class="close-modal" style="margin-top: 20px;">Fechar</button>
      </div>
  `;
  document.body.appendChild(modal);

   // Close modal button
  modal.querySelector('.close-modal').addEventListener('click', () => {
      modal.remove();
  });

  // In a real app, you'd start polling a backend to check payment status here
};

const showCardPaymentModal = (paymentDetails) => {
  // Basic modal creation - requires CSS for styling
  const modal = document.createElement('div');
  modal.className = 'payment-modal card-modal'; // Add a class for specific styling
  modal.innerHTML = `
      <div class="payment-modal-content">
          <h3>Pagamento com Cartão</h3>
          <form id="cardPaymentForm">
              <input type="text" placeholder="Número do Cartão" required>
              <input type="text" placeholder="MM/AA" required>
              <input type="text" placeholder="CVC" required>
              <button type="submit">Pagar ${paymentDetails.amount} ${paymentDetails.currency}</button>
          </form>
           <button class="close-modal" style="margin-top: 20px;">Fechar</button>
      </div>
  `;
  document.body.appendChild(modal);

  // Close modal button
  modal.querySelector('.close-modal').addEventListener('click', () => {
      modal.remove();
  });

  // Handle form submission (simulated)
  modal.querySelector('#cardPaymentForm').addEventListener('submit', async (event) => {
      event.preventDefault();
      // Simulate card payment processing
      const statusElement = modal.querySelector('.payment-status'); // Assuming you add a status element to the form
      // if (!statusElement) { // Add one if not present
      //      statusElement = document.createElement('div');
      //      statusElement.className = 'payment-status';
      //      modal.querySelector('.payment-modal-content').appendChild(statusElement);
      // }
      // statusElement.textContent = 'Processando...';

      await new Promise(resolve => setTimeout(resolve, 1500)); // Simulate delay

      // Simulate success or failure
      const success = Math.random() > 0.1; // 90% success rate

      if (success) {
           showUxMessage('payment_identified', 'success');
           modal.remove(); // Close modal on success
           // In a real app, you'd then trigger the crypto transaction execution
           // For this integration, the verifyPayment loop handles the next step.
           // We need a way for the modal's success state to signal verifyPayment.
           // A simpler approach for this simulation is to have processTransaction wait.
      } else {
           showUxMessage('payment_failed', 'error');
           // statusElement.textContent = 'Falha';
           // Keep modal open or close based on UX
      }
  });
};

const handlePaymentMethod = async (paymentDetails) => {
  // This function now just *shows* the correct modal.
  // The verification loop is started *after* this call in processTransaction.
  switch (paymentDetails.method) {
      case 'pix':
          paymentDetails.pixCode = await generatePixCode(paymentDetails); // Generate code before showing
          showPixPaymentModal(paymentDetails);
          break;
      case 'card':
          showCardPaymentModal(paymentDetails); // Card modal handles its own simulated form submit
          break;
      default:
          throw new Error('Método de pagamento não suportado.'); // Portuguese
  }
};

const checkPaymentStatus = async (paymentId) => {
  // Simulate checking payment status
  // In a real app, this would poll a backend API
  return new Promise((resolve) => {
      setTimeout(() => {
           // Simulate status: 80% completed, 10% pending, 10% failed
          const rand = Math.random();
          if (rand < 0.8) {
              resolve('completed');
          } else if (rand < 0.9) {
              resolve('pending');
          } else {
              resolve('failed');
          }
      }, 2000); // Simulate polling delay
  });
};

const verifyPayment = async (paymentId) => {
  // This function waits for payment confirmation via checkPaymentStatus
  let attempts = 0;
  const maxAttempts = 30; // Poll for up to 60 seconds (30 * 2s)
  const pollInterval = 2000; // Poll every 2 seconds
  const statusElement = document.querySelector('.payment-modal .payment-status'); // Find the status element in the open modal

  const verifyLoop = async () => {
      if (attempts >= maxAttempts) {
          if (statusElement) statusElement.textContent = 'Pagamento expirou.'; // Portuguese
          // Need to close the modal here
          const modal = document.querySelector('.payment-modal');
          if(modal) modal.remove();
          throw new Error('Tempo limite para confirmação do pagamento excedido.'); // Portuguese
      }

      if (statusElement) statusElement.textContent = `Aguardando confirmação... Tentativa ${attempts + 1}/${maxAttempts}`; // Portuguese

      const status = await checkPaymentStatus(paymentId);

      if (status === 'completed') {
          if (statusElement) statusElement.textContent = 'Pagamento confirmado!'; // Portuguese
          // Need to close the modal here
          const modal = document.querySelector('.payment-modal');
          if(modal) modal.remove();
          console.log("Payment verified, executing transaction...");
          const tx = await executeTransaction(); // Execute crypto transaction
          showSuccessNotification(tx); // Show success message
          // Transaction completed, the processTransaction will handle the next step (resetting flow)
      } else if (status === 'failed') {
           if (statusElement) statusElement.textContent = 'Pagamento falhou.'; // Portuguese
           // Need to close the modal here
           const modal = document.querySelector('.payment-modal');
           if(modal) modal.remove();
          throw new Error('O pagamento falhou.'); // Portuguese
      } else { // 'pending'
          attempts++;
          setTimeout(verifyLoop, pollInterval); // Continue polling
      }
  };
  await verifyLoop(); // Start the loop
};


const showSuccessNotification = (tx) => {
  // Basic notification - requires CSS
  const div = document.createElement('div');
  div.className = 'transaction-notification success';
  div.innerHTML = `
      <div class="notification-content">
          <span class="check-icon">✓</span>
          <div>
              <h4>Pagamento Confirmado e Crypto Enviada!</h4>
              <p>Hash: ${tx.txHash.substring(0, 10)}...</p> </div>
      </div>
  `;
  document.body.appendChild(div);
  setTimeout(() => div.remove(), 8000); // Show for 8 seconds
};

// Hero animations (premium title typing)
function initHeroAnimations() {
  if (typeof anime === 'undefined') return;

  const premiumTitle = document.getElementById('premiumTitle');
  if (premiumTitle) {
      const mobileTitleContent = premiumTitle.dataset.textMobile;
      const titleContent = window.matchMedia('(max-width: 768px)').matches && mobileTitleContent
          ? mobileTitleContent
          : premiumTitle.textContent.trim();
      premiumTitle.textContent = ''; // Clear to start typing

      const typeAnim = anime({
          targets: { value: 0 },
          value: titleContent.length,
          duration: 2000, // typing speed
          delay: 1000,    // start after a short pause
          endDelay: 3000, // rest 3s before repeating
          loop: true,
          easing: 'linear',
          begin: function() {
              premiumTitle.classList.remove('typing-finished');
              premiumTitle.textContent = '';
          },
          update: function(anim) {
              const charIndex = Math.floor(anim.animatables[0].target.value);
              premiumTitle.textContent = titleContent.substring(0, charIndex);
          },
          loopComplete: function() {
              premiumTitle.classList.add('typing-finished');
          }
      });

  }
}

const processTransaction = async () => {
  // This function is called when 'Continue' is clicked on Step 4
  const loadingSpinner = document.querySelector('.loading-spinner'); // Assume spinner exists

  // Basic validation again before starting
  if (!state.walletAddress || !validateWalletAddress(state.walletAddress)) {
      showUxMessage('invalid_wallet', 'warning');
      return; // Stop if validation fails
  }

  if (!state.selectedPaymentMethod) {
      showUxMessage('payment_method_required', 'warning');
       return; // Stop if no method selected
  }

  // No need to validate amount here, as it was validated in Step 1

  const paymentId = 'PAY-' + Math.random().toString(36).substr(2, 9).toUpperCase();
  const paymentDetails = {
      id: paymentId,
      amount: state.payAmount,
      currency: state.payCurrency,
      method: state.selectedPaymentMethod.dataset.method,
      status: 'pending', // Initial status
      timestamp: Date.now()
  };

  // Show loading spinner while modal is prepared/shown and payment is processed
  if (loadingSpinner) loadingSpinner.classList.remove('hidden');

  try {
      // handlePaymentMethod shows the modal for PIX or Card
      await handlePaymentMethod(paymentDetails);

      // verifyPayment starts the polling loop. It will execute executeTransaction on success.
      await verifyPayment(paymentId);

      // If verifyPayment completes without throwing an error, it means the payment was confirmed
      // and executeTransaction ran successfully.
      // The flow will be reset by the continueBtn handler after this promise resolves.

  } catch (err) {
      console.error("Erro no processamento da transação:", err);
      showUxMessage(err.message, 'error');
      // Close any open modal on error
      const modal = document.querySelector('.payment-modal');
      if(modal) modal.remove();
       // The continueBtn handler for step 4 should handle the reset after this catch block.
  } finally {
      // Hide spinner regardless of success or failure
      if (loadingSpinner) loadingSpinner.classList.add('hidden');
      // The continueBtn handler for step 4 now knows this is done and will reset.
  }
};


// --- UI Management (from second block, adapted and integrated with first block's logic) ---

const updateReceiveAmount = () => {
  // Function to calculate and update receive amount based on pay amount and rate
  const payAmountInput = document.getElementById('payAmount');
  const receiveAmountInput = document.getElementById('receiveAmount');

  const inputValue = parseFloat(payAmountInput.value);
  const selectedPool = LIQUIDITY_POOLS[state.receiveCurrency] || LIQUIDITY_POOLS[state.payCurrency] || LIQUIDITY_POOLS.USDT;
  const rate = state.action === 'sell'
    ? getSellAssetBrlPrice(state.payCurrency)
    : Number(selectedPool?.price || state.exchangeRate || 0);

  // Only calculate if exchange rate is available and input is a valid positive number
  if (!isNaN(inputValue) && inputValue > 0 && rate > 0) {
      const brlInputValue = convertFiatToBrl(inputValue, state.payCurrency);
      const value = state.action === 'sell' ? convertBrlToFiat(inputValue * rate, state.receiveCurrency) : brlInputValue / rate;
      const precision = state.action === 'sell' ? 2 : state.receiveCurrency === 'BTC' ? 8 : 6;
      receiveAmountInput.value = value.toFixed(precision);
  } else {
      receiveAmountInput.value = '';
  }
};

const readPositiveNumber = (...values) => {
  for (const value of values) {
    const number = Number(value);
    if (Number.isFinite(number) && number > 0) return number;
  }
  return 0;
};

const setFiatRateToBrl = (currency, value) => {
  const code = String(currency || '').toUpperCase();
  const rate = readPositiveNumber(value);
  if (code && rate > 0) FIAT_RATES_TO_BRL[code] = rate;
};

const applyPriceSnapshot = (snapshot) => {
  if (!snapshot || !snapshot.rates || snapshot.rates.USDTBRL <= 0) {
    throw new Error('cotacao invalida');
  }

  priceState.source = snapshot.source || 'unknown';
  priceState.fetchedAt = snapshot.fetchedAt || Date.now();
  priceState.sellWallet = snapshot.sellWallet || priceState.sellWallet;
  priceState.sellNetworks = normalizeSellNetworks(snapshot.sellNetworks || priceState.sellNetworks);
  priceState.sellNetwork = normalizeSellNetwork(snapshot.sellNetwork || priceState.sellNetwork);
  state.sellNetwork = normalizeSellNetwork(state.sellNetwork || priceState.sellNetwork);
  if (!priceState.sellNetworks.includes(state.sellNetwork)) {
    state.sellNetwork = priceState.sellNetworks[0];
  }
  priceState.sellNetwork = state.sellNetwork;
  priceState.rates = { ...priceState.rates, ...snapshot.rates };

  state.exchangeRate = priceState.rates.USDTBRL;
  LIQUIDITY_POOLS.USDT.price = priceState.rates.USDTBRL;
  LIQUIDITY_POOLS.USDT.sellPrice = priceState.rates.SELLUSDTBRL || priceState.rates.USDTBRL;
  LIQUIDITY_POOLS.EURUSDT.price = priceState.rates.EURBRL || priceState.rates.USDTBRL;
  LIQUIDITY_POOLS.BTC.price = priceState.rates.BTCBRL || LIQUIDITY_POOLS.BTC.price;

  FIAT_RATES_TO_BRL.BRL = 1;
  setFiatRateToBrl('USD', priceState.rates.USDTBRL);
  setFiatRateToBrl('EUR', priceState.rates.EURBRL);
  updateSellDepositWallet();
};

const convertFiatToBrl = (value, currency) => {
  const rate = FIAT_RATES_TO_BRL[currency] || 0;
  return rate > 0 ? (Number(value) || 0) * rate : 0;
};

const convertBrlToFiat = (value, currency) => {
  const rate = FIAT_RATES_TO_BRL[currency] || 0;
  return rate > 0 ? (Number(value) || 0) / rate : 0;
};

const formatFiat = (value, currency = 'BRL') => {
  const amount = Number(value) || 0;
  const code = String(currency || 'BRL').toUpperCase();
  if (!['BRL', 'USD', 'EUR'].includes(code)) {
    return amount.toLocaleString('en-US', { maximumFractionDigits: 8 });
  }
  const locale = code === 'BRL' ? 'pt-BR' : 'en-US';
  return amount.toLocaleString(locale, { style: 'currency', currency: code });
};

const formatBrl = (value) => {
  const amount = Number(value) || 0;
  return amount.toLocaleString('pt-BR', { style: 'currency', currency: 'BRL' });
};

const normalizeSellNetwork = (network) => {
  const value = String(network || '').trim().toUpperCase();
  if (!value || value === 'BEP20' || value === 'BINANCE') return 'BSC';
  if (value === 'POL' || value === 'MATIC') return 'POLYGON';
  return SELL_NETWORKS[value] ? value : 'BSC';
};

const normalizeSellNetworks = (networks) => {
  const values = Array.isArray(networks) ? networks : String(networks || '').split(',');
  const normalized = values
    .map(normalizeSellNetwork)
    .filter((network, index, list) => SELL_NETWORKS[network] && list.indexOf(network) === index);
  return normalized.length > 0 ? normalized : ['BSC'];
};

const getSellNetworkMeta = (network = state.sellNetwork) => {
  return SELL_NETWORKS[normalizeSellNetwork(network)] || SELL_NETWORKS.BSC;
};

const getSellAssetBrlPrice = (asset) => {
  const code = String(asset || '').toUpperCase();
  if (code === 'USDT') {
    return priceState.rates.SELLUSDTBRL || LIQUIDITY_POOLS.USDT.sellPrice || priceState.rates.USDTBRL || state.exchangeRate || 0;
  }
  return Number((LIQUIDITY_POOLS[code] || {}).price || 0);
};

const updateSellDepositWallet = () => {
  const enabledNetworks = normalizeSellNetworks(priceState.sellNetworks);
  if (!enabledNetworks.includes(normalizeSellNetwork(state.sellNetwork))) {
    state.sellNetwork = enabledNetworks[0];
    priceState.sellNetwork = state.sellNetwork;
  }
  const walletEl = document.getElementById('sellDepositWallet');
  if (walletEl) walletEl.textContent = priceState.sellWallet;
  const meta = getSellNetworkMeta();
  const iconEl = document.getElementById('sellDepositNetworkIcon');
  const labelEl = document.getElementById('sellDepositNetworkLabel');
  const selectEl = document.getElementById('sellNetwork');
  if (iconEl) {
    iconEl.src = meta.icon;
    iconEl.alt = meta.shortLabel;
  }
  if (labelEl) labelEl.textContent = meta.displayLabel;
  if (selectEl) {
    Array.from(selectEl.options || []).forEach(option => {
      const network = normalizeSellNetwork(option.value);
      option.disabled = !enabledNetworks.includes(network);
    });
    selectEl.value = meta.code;
  }
  document.querySelectorAll('.sell-network-option[data-network]').forEach(button => {
    const network = normalizeSellNetwork(button.dataset.network);
    const enabled = enabledNetworks.includes(network);
    const selected = enabled && network === meta.code;
    button.disabled = !enabled;
    button.classList.toggle('disabled', !enabled);
    button.classList.toggle('active', selected);
    button.setAttribute('aria-checked', selected ? 'true' : 'false');
    button.setAttribute('aria-disabled', enabled ? 'false' : 'true');
  });
};

const syncMarketsFromPriceState = () => {
  const marketPrices = {
    USDT: priceState.rates.USDTBRL,
    EURUSDT: priceState.rates.EURBRL,
    BTC: priceState.rates.BTCBRL
  };

  document.querySelectorAll('.markets-row[data-market-category]').forEach(row => {
    const asset = row.querySelector('.market-asset strong')?.textContent?.trim();
    const price = marketPrices[asset];
    if (!asset || !price || price <= 0) return;

    const priceLabel = formatBrl(price);
    const priceEl = row.querySelector('.market-price');
    const pairEl = priceEl?.nextElementSibling;
    const statusEl = row.querySelector('.market-status');

    if (priceEl) priceEl.textContent = priceLabel;
    if (pairEl) pairEl.textContent = `1 ${asset} = ${priceLabel}`;
    if (statusEl) statusEl.textContent = priceState.source === 'backend' ? 'Live' : 'Fallback';
  });
};

const normalizeQrImageSrc = (value) => {
  const src = String(value || '').trim();
  if (!src) return '';
  if (/^(data:image\/|https?:\/\/|\/)/i.test(src)) return src;
  return `data:image/png;base64,${src}`;
};

const getReceiveDisplayValue = () => {
  const receiveAmountInput = document.getElementById('receiveAmount');
  let amount = receiveAmountInput?.value || '';
  const selectedPool = LIQUIDITY_POOLS[state.receiveCurrency] || LIQUIDITY_POOLS[state.payCurrency] || LIQUIDITY_POOLS.USDT;
  const rate = state.action === 'sell'
    ? getSellAssetBrlPrice(state.payCurrency)
    : Number(selectedPool?.price || state.exchangeRate || 0);
  if (!amount && state.payAmount > 0 && rate > 0) {
      const brlInputValue = convertFiatToBrl(state.payAmount, state.payCurrency);
      amount = state.action === 'sell'
        ? convertBrlToFiat(state.payAmount * rate, state.receiveCurrency).toFixed(2)
        : (brlInputValue / rate).toFixed(8);
      if (receiveAmountInput) receiveAmountInput.value = amount;
  }
  return amount ? `${amount} ${state.receiveCurrency}` : `0 ${state.receiveCurrency}`;
};

const updateOrderSummaries = () => {
  const payText = state.action === 'sell'
    ? `${formatFiat(state.payAmount, state.payCurrency)} ${state.payCurrency}`
    : `${formatFiat(state.payAmount, state.payCurrency)} ${state.payCurrency}`;
  const totalText = state.action === 'sell'
    ? `${formatFiat(state.payAmount, state.payCurrency)} ${state.payCurrency}`
    : `${formatFiat(state.totalPayAmount || state.payAmount, state.payCurrency)} ${state.payCurrency}`;
  const receiveText = getReceiveDisplayValue();

  document.querySelectorAll('#displayPayAmountStep2, #displayPayAmountStep3').forEach(el => {
      el.textContent = payText;
  });
  document.querySelectorAll('#displayReceiveAmountStep2, #displayReceiveAmountStep3').forEach(el => {
      el.textContent = receiveText;
  });

  const displayTotalStep3 = document.getElementById('displayTotalStep3');
  if (displayTotalStep3) displayTotalStep3.textContent = totalText;
};

const CARD_CHECKOUT_STEP_COUNT = 4;

const setCardStatus = (message, type = '') => {
  if (type === 'warn' || type === 'error') showUxMessage(message, type === 'warn' ? 'warning' : type);
};

const setCardCheckoutStep = (step, focus = false) => {
  state.cardCheckoutStep = Math.max(1, Math.min(CARD_CHECKOUT_STEP_COUNT, Number(step) || 1));
  document.querySelectorAll('[data-card-step]').forEach(panel => {
    panel.classList.toggle('hidden', Number(panel.dataset.cardStep) !== state.cardCheckoutStep);
  });
  const continueBtn = document.getElementById('continueBtn');
  if (continueBtn && state.currentStep === 3 && selectedPaymentRail().paymentMethod === 'credit_card') {
    continueBtn.textContent = state.cardCheckoutStep === CARD_CHECKOUT_STEP_COUNT ? 'Pagar com cartao' : 'Avancar';
  }
  if (focus) {
    const currentPanel = document.querySelector(`[data-card-step="${state.cardCheckoutStep}"]`);
    setTimeout(() => currentPanel?.querySelector('input')?.focus(), 0);
  }
};

const validateCardCheckoutStep = (step = state.cardCheckoutStep) => {
  const expiry = splitExpiry(inputValue('cardExpiry'));
  const validators = {
    1: [
      [digitsOnly(inputValue('cardNumber')).length >= 13, 'Informe o numero do cartao.'],
      [inputValue('cardHolderName').length >= 3, 'Informe o nome impresso.'],
      [expiry.month.length === 2 && expiry.year.length === 4, 'Informe a validade MM/AA.'],
      [digitsOnly(inputValue('cardCvv')).length >= 3, 'Informe o CVV.']
    ],
    2: [
      [digitsOnly(inputValue('cardCpf')).length === 11, 'Informe o CPF.'],
      [digitsOnly(inputValue('cardPhone')).length >= 10, 'Informe o telefone.'],
      [/^\S+@\S+\.\S+$/.test(inputValue('cardEmail')), 'Informe um email valido.']
    ],
    3: [
      [Boolean(inputValue('cardBirthDate')), 'Informe a data de nascimento.']
    ],
    4: [
      [Boolean(inputValue('billingStreet')), 'Informe o endereco de cobranca.'],
      [Boolean(inputValue('billingNumber')), 'Informe o numero.'],
      [Boolean(inputValue('billingNeighborhood')), 'Informe o bairro.'],
      [digitsOnly(inputValue('billingZipcode')).length === 8, 'Informe o CEP com 8 digitos.'],
      [Boolean(inputValue('billingCity')), 'Informe a cidade.'],
      [inputValue('billingState').length === 2, 'Informe a UF.']
    ]
  };
  const failed = (validators[step] || []).find(([valid]) => !valid);
  if (failed) {
    setCardStatus(failed[1], 'warn');
    return false;
  }
  return true;
};

const advanceCreditCardCheckout = () => {
  if (!validateCardCheckoutStep()) return false;
  if (state.cardCheckoutStep < CARD_CHECKOUT_STEP_COUNT) {
    setCardCheckoutStep(state.cardCheckoutStep + 1, true);
    return false;
  }
  return true;
};

const updateStep3PaymentPreview = (showCardWarning = false) => {
  const pixPanel = document.getElementById('step3PixPanel');
  const cardMessage = document.getElementById('step3CardMessage');
  const cardPanel = document.getElementById('step3CardPanel');
  const cardBrandIcon = document.getElementById('step3CardBrandIcon');
  const bypassBtn = document.getElementById('step3BypassBtn');
  const step3ConfirmPaymentBtn = document.getElementById('step3ConfirmPaymentBtn');
  const continueBtn = document.getElementById('continueBtn');
  const rail = selectedPaymentRail();
  const isPix = rail.method === 'pix';
  const isCard = rail.paymentMethod === 'credit_card';

  if (pixPanel) pixPanel.classList.toggle('hidden', !isPix);
  if (cardPanel) cardPanel.classList.toggle('hidden', !isCard);
  if (bypassBtn) bypassBtn.classList.toggle('hidden', !isPix);
  if (step3ConfirmPaymentBtn) step3ConfirmPaymentBtn.classList.toggle('hidden', !isPix);
  if (continueBtn && state.currentStep === 3) {
      continueBtn.classList.toggle('hidden', isPix);
      if (isCard) setCardCheckoutStep(state.cardCheckoutStep || 1);
  }
  if (cardBrandIcon && isCard) {
      const selectedIcon = state.selectedPaymentMethod?.querySelector('img');
      cardBrandIcon.src = selectedIcon?.getAttribute('src') || (rail.cardBrand === 'mastercard' ? '/images/mastercard.png' : '/images/visa.png');
      cardBrandIcon.alt = selectedIcon?.alt || (rail.cardBrand === 'mastercard' ? 'Mastercard' : 'Visa');
  }
  if (cardMessage) {
      cardMessage.classList.toggle('hidden', isPix || isCard || !showCardWarning);
      if (!isPix && !isCard && showCardWarning) cardMessage.textContent = 'Metodo indisponivel.';
  }
};

const updateFinalPaymentStatus = (status) => {
  const paymentStatusLabel = document.getElementById('paymentStatusLabel');
  if (!paymentStatusLabel) return;

  const normalized = String(status || '').toLowerCase();
  const isComplete = ['complete', 'completed', 'paid', 'pago', 'confirmed', 'enviado', 'finalizado', 'concluido', 'concluído'].some(value => normalized.includes(value));
  paymentStatusLabel.textContent = isComplete ? 'Pagamento completo' : 'Pagamento identificado';
};

const resolveApiBase = () => {
  const configured =
    window.SWAPPED_API_BASE_URL ||
    import.meta.env.VITE_SWAPPED_API_BASE_URL ||
    localStorage.getItem('SWAPPED_API_BASE_URL') ||
    'https://api-production-bc748.up.railway.app';
  return String(configured).trim().replace(/\/+$/, '');
};

const resolveEfiPayeeCode = () => {
  return String(
    window.SWAPPED_EFI_PAYEE_CODE ||
    import.meta.env.VITE_EFI_PAYEE_CODE ||
    localStorage.getItem('SWAPPED_EFI_PAYEE_CODE') ||
    ''
  ).trim();
};

const resolveEfiEnvironment = () => {
  const configured = String(
    window.SWAPPED_EFI_ENVIRONMENT ||
    import.meta.env.VITE_EFI_ENVIRONMENT ||
    localStorage.getItem('SWAPPED_EFI_ENVIRONMENT') ||
    ''
  ).trim().toLowerCase();
  if (configured === 'production' || configured === 'sandbox') return configured;
  return location.hostname === 'localhost' || location.hostname === '127.0.0.1' ? 'sandbox' : 'production';
};

const isLocalDevBypassEnabled = () => {
  return ['localhost', '127.0.0.1'].includes(location.hostname);
};

const normalizeCardBrand = (method) => {
  switch (String(method || '').toLowerCase()) {
    case 'visa':
      return 'visa';
    case 'master':
    case 'mastercard':
      return 'mastercard';
    default:
      return '';
  }
};

const selectedPaymentRail = () => {
  const method = state.selectedPaymentMethod?.dataset?.method || '';
  const brand = normalizeCardBrand(method);
  return {
    method,
    paymentMethod: brand ? 'credit_card' : method,
    cardBrand: brand
  };
};

const digitsOnly = (value) => String(value || '').replace(/\D/g, '');

const formatCPF = (value) => {
  const digits = digitsOnly(value).slice(0, 11);
  return digits
    .replace(/^(\d{3})(\d)/, '$1.$2')
    .replace(/^(\d{3})\.(\d{3})(\d)/, '$1.$2.$3')
    .replace(/^(\d{3})\.(\d{3})\.(\d{3})(\d)/, '$1.$2.$3-$4');
};

const isValidCPF = (value) => {
  const cpf = digitsOnly(value);
  if (cpf.length !== 11 || /^(\d)\1{10}$/.test(cpf)) return false;
  const checkDigit = (position) => {
    let sum = 0;
    let weight = position + 1;
    for (let i = 0; i < position; i += 1) {
      sum += Number(cpf[i]) * weight;
      weight -= 1;
    }
    const rest = (sum * 10) % 11;
    return rest === 10 ? 0 : rest;
  };
  return checkDigit(9) === Number(cpf[9]) && checkDigit(10) === Number(cpf[10]);
};

const splitExpiry = (value) => {
  const digits = digitsOnly(value);
  if (digits.length < 4) return { month: '', year: '' };
  const month = digits.slice(0, 2);
  const rawYear = digits.slice(2, 6);
  const year = rawYear.length === 2 ? `20${rawYear}` : rawYear;
  return { month, year };
};

const inputValue = (id) => document.getElementById(id)?.value?.trim() || '';

const collectCardCheckoutInput = () => {
  const rail = selectedPaymentRail();
  const expiry = splitExpiry(inputValue('cardExpiry'));
  const billingAddress = {
    street: inputValue('billingStreet'),
    number: inputValue('billingNumber'),
    neighborhood: inputValue('billingNeighborhood'),
    zipcode: digitsOnly(inputValue('billingZipcode')),
    city: inputValue('billingCity'),
    state: inputValue('billingState').toUpperCase()
  };
  const customer = {
    name: inputValue('cardHolderName'),
    cpf: digitsOnly(inputValue('cardCpf')),
    email: inputValue('cardEmail'),
    phone: digitsOnly(inputValue('cardPhone')),
    birthDate: inputValue('cardBirthDate'),
    address: billingAddress
  };
  return {
    rail,
    card: {
      brand: rail.cardBrand,
      number: digitsOnly(inputValue('cardNumber')),
      cvv: digitsOnly(inputValue('cardCvv')),
      expirationMonth: expiry.month,
      expirationYear: expiry.year,
      holderName: customer.name,
      holderDocument: customer.cpf
    },
    customer,
    billingAddress
  };
};

const validateCardCheckoutInput = (checkout) => {
  const requiredCard = [
    checkout.card.brand,
    checkout.card.number,
    checkout.card.cvv,
    checkout.card.expirationMonth,
    checkout.card.expirationYear,
    checkout.customer.name,
    checkout.customer.cpf,
    checkout.customer.email,
    checkout.customer.phone,
    checkout.billingAddress.street,
    checkout.billingAddress.number,
    checkout.billingAddress.neighborhood,
    checkout.billingAddress.zipcode,
    checkout.billingAddress.city,
    checkout.billingAddress.state
  ];
  if (requiredCard.some(value => !String(value || '').trim())) return false;
  if (checkout.customer.cpf.length !== 11) return false;
  if (checkout.customer.phone.length < 10) return false;
  if (checkout.billingAddress.zipcode.length !== 8) return false;
  if (!/^\S+@\S+\.\S+$/.test(checkout.customer.email)) return false;
  return true;
};

const generateEfiPaymentToken = async (checkout) => {
  const payeeCode = resolveEfiPayeeCode();
  const efiCard = window.EfiPay?.CreditCard || window.EfiJs?.CreditCard;
  if (!payeeCode || !efiCard) {
    throw new Error('card_token_unavailable');
  }
  if (typeof efiCard.isScriptBlocked === 'function' && await efiCard.isScriptBlocked()) {
    throw new Error('card_token_unavailable');
  }
  const tokenResult = await efiCard
    .setEnvironment(resolveEfiEnvironment())
    .setAccount(payeeCode)
    .setCreditCardData({
      brand: checkout.card.brand,
      number: checkout.card.number,
      cvv: checkout.card.cvv,
      expirationMonth: checkout.card.expirationMonth,
      expirationYear: checkout.card.expirationYear,
      holderName: checkout.card.holderName,
      holderDocument: checkout.card.holderDocument,
      reuse: false
    })
    .getPaymentToken();
  const paymentToken = tokenResult?.payment_token || tokenResult?.data?.payment_token || '';
  if (!paymentToken) throw new Error('card_token_unavailable');
  return paymentToken;
};

const normalizePriceSnapshot = (data, source = 'backend') => {
  const rates = data?.rates || data || {};
  const usdtBrl = readPositiveNumber(
    data?.brl,
    data?.BRL,
    data?.priceBRL,
    data?.rate,
    data?.usdtbrl,
    data?.USDTBRL,
    data?.tether?.brl,
    data?.usdt?.brl,
    rates?.USDT_BRL,
    rates?.USDTBRL,
    rates?.BRL
  );
  const usdtUsd = readPositiveNumber(data?.usd, data?.USDTUSD, data?.tether?.usd, rates?.USDT_USD, rates?.USDTUSD, rates?.USD, 1);
  const sellUsdtBrl = readPositiveNumber(data?.sellUsdtBrl, data?.SELLUSDTBRL, data?.sellUSDTBRL, rates?.SELL_USDT_BRL, rates?.SELLUSDTBRL);
  const usdtEur = readPositiveNumber(data?.eur, data?.USDTEUR, data?.tether?.eur, rates?.USDT_EUR, rates?.USDTEUR, rates?.EUR);
  const eurUsd = readPositiveNumber(data?.eurusd, data?.EURUSD, rates?.EUR_USD, rates?.EURUSD);
  const btcUsdt = readPositiveNumber(data?.btcusdt, data?.BTCUSDT, rates?.BTC_USDT, rates?.BTCUSDT);
  const eurBrl = readPositiveNumber(
    data?.eurbrl,
    data?.EURBRL,
    rates?.EUR_BRL,
    rates?.EURBRL,
    usdtBrl > 0 && eurUsd > 0 ? usdtBrl * eurUsd : 0,
    usdtBrl > 0 && usdtUsd > 0 && usdtEur > 0 ? usdtBrl * (usdtUsd / usdtEur) : 0
  );

  return {
    source,
    fetchedAt: Date.now(),
    sellWallet: data?.sellWallet || data?.sellWalletAddress || data?.SELL_WALLET_ADDRESS || rates?.sellWallet || rates?.SELL_WALLET_ADDRESS,
    sellNetwork: data?.sellNetwork || data?.SELL_NETWORK || rates?.sellNetwork || 'BEP20',
    sellNetworks: data?.sellNetworks || data?.supportedSellNetworks || rates?.sellNetworks || rates?.supportedSellNetworks,
    rates: {
      USDTBRL: usdtBrl,
      SELLUSDTBRL: sellUsdtBrl || usdtBrl,
      USD: usdtUsd,
      EUR: usdtEur,
      EURBRL: eurBrl,
      BTCUSDT: btcUsdt,
      BTCBRL: usdtBrl > 0 && btcUsdt > 0 ? usdtBrl * btcUsdt : 0
    }
  };
};

const updateStep = (step) => {
  // Function to manage which step is visible and update indicators/buttons
  state.currentStep = step;

  // Hide all step content divs
  document.querySelectorAll('.step-content').forEach(el => el.classList.add('hidden'));

  // Show the content for the current step
  const currentStepElement = document.querySelector(`.step-${step}`);
  if (currentStepElement) {
      currentStepElement.classList.remove('hidden');
  } else {
      console.error(`Step content for step ${step} not found.`);
      // Optionally reset to step 1 or show an error state
      return;
  }


  // Update the step indicator text (assuming a div with class 'steps-indicator')
  const stepsIndicator = document.querySelector('.steps-indicator');
  if (stepsIndicator) {
      stepsIndicator.innerText = `Passo ${step} de ${Object.keys(steps).length}: ${steps[step]}`; // Portuguese
  } else {
       console.warn("Element with class 'steps-indicator' not found.");
  }

  // Update continue button text based on the step
  const continueBtn = document.getElementById('continueBtn');
  if (continueBtn) {
      const actionLabel = state.action === 'sell' ? 'Sell Now' : 'Buy Now';
      continueBtn.classList.remove('hidden');
      if (step === 4) {
          continueBtn.innerText = 'Processar Pagamento'; // Portuguese
      } else if (step === 3) {
          continueBtn.innerText = 'Avançar';
      } else if (step === 5) {
          continueBtn.innerText = 'Início';
          continueBtn.classList.add('hidden');
      } else {
          continueBtn.innerText = actionLabel;
      }
  }


   // Manage card header visibility - Hide it on the confirmation step (step 4)
   const cardHeader = document.querySelector('.card-header');
   if(cardHeader) {
       if (step === 5 && state.action !== 'sell') {
           cardHeader.classList.add('hidden');
       } else {
           cardHeader.classList.remove('hidden');
       }
   }

  // Optional: Perform actions specific to entering a step
  if (step === 2) {
       updateOrderSummaries();
       updateSellDepositWallet();
       syncBuyPayerInfo();
       // Maybe focus the wallet input or show a connect button
       const walletInput = document.getElementById('walletAddress');
       if (walletInput && !state.connected) {
            // Don't auto-connect, let the user click 'Continue' again in step 2 to connect
            // walletInput.focus();
            // Or update UI to show 'Connect Wallet' button if not connected
       }
  } else if (step === 3) {
      updateOrderSummaries();
      updateStep3PaymentPreview(false);
  } else if (step === 5) {
      // Populate the confirmation details (already done in continueBtn handler, but could be here)
      const paymentBtcAmountDisplay = document.getElementById('paymentBtcAmount');
      const paymentMethodDisplay = document.getElementById('paymentMethod');
      const paymentWalletDisplay = document.getElementById('paymentWallet');
      const paymentStatusLabel = document.getElementById('paymentStatusLabel');

      if (state.action === 'sell') {
        if (paymentStatusLabel) paymentStatusLabel.textContent = paymentStatusLabel.textContent || 'Aguardando deposito';
        if (paymentBtcAmountDisplay) paymentBtcAmountDisplay.textContent = `${(state.payAmount || 0).toFixed(6)} USDT`;
        if (paymentWalletDisplay && !paymentWalletDisplay.textContent) paymentWalletDisplay.textContent = getReceiveDisplayValue();
      } else {
        const paymentInfoSection = document.getElementById('paymentInfoSection');
        if (paymentInfoSection) paymentInfoSection.classList.remove('sell-deposit-pending');
        if (paymentStatusLabel) paymentStatusLabel.textContent = 'Pagamento identificado';
        if (paymentBtcAmountDisplay) paymentBtcAmountDisplay.textContent = getReceiveDisplayValue();
        if (paymentMethodDisplay && state.selectedPaymentMethod) paymentMethodDisplay.textContent = state.selectedPaymentMethod.dataset.method.toUpperCase();
        if (paymentWalletDisplay) paymentWalletDisplay.textContent = state.walletAddress;
      }
  }
};

// --- DOMContentLoaded Listener (combining setup and initial logic) ---

document.addEventListener('DOMContentLoaded', async () => {
  const API_BASE = resolveApiBase();
  let buySse = null;
  let buyPoll = null;
  let currentBuyId = null;
  let currentBuyAccessToken = null;
  let currentBuyTxHash = null;
  let buyOrderPromise = null;
  let sellSse = null;
  let sellPoll = null;
  let currentSellId = null;
  let currentSellAccessToken = null;
  const PARTICLE_ICON = 'https://res.cloudinary.com/limpeja/image/upload/v1771076927/iconnn-Photoroom_wdsmis.png';

  // Get DOM element references
  const continueBtn = document.getElementById('continueBtn');
  const confirmPaymentBtn = document.getElementById('confirmPaymentBtn');
  const step3ConfirmPaymentBtn = document.getElementById('step3ConfirmPaymentBtn');
  const step3BypassBtn = document.getElementById('step3BypassBtn');
  
  const orderInfoBox = document.getElementById('orderInfoBox');
  const payAmountInput = document.getElementById('payAmount');
  const receiveAmountInput = document.getElementById('receiveAmount');
  const payAmountLabel = document.querySelector('label[for="payAmount"]');
  const receiveAmountLabel = document.querySelector('label[for="receiveAmount"]');
  const rateText = document.querySelector('.rate-text');
  const rateSpinner = document.querySelector('.loading-spinner');
  const paymentMethodButtons = document.querySelectorAll('.payment-method');
  const walletAddressInput = document.getElementById('walletAddress'); // destino on-chain
  const orderStatusEl = document.getElementById('orderStatus');
  const orderIdEl = document.getElementById('orderId');
  const depositAddressEl = document.getElementById('depositAddress');
  const sellDepositAddressInput = document.getElementById('sellDepositAddressInput');
  const sellDepositCopyBtn = document.getElementById('sellDepositCopyBtn');
  const sellDepositBlock = document.getElementById('sellDepositBlock');
  const sellNetworkSelect = document.getElementById('sellNetwork');
  const sellNetworkOptions = document.querySelectorAll('.sell-network-option[data-network]');
  const paymentBtcAmountEl = document.getElementById('paymentBtcAmount');
  const paymentWalletEl = document.getElementById('paymentWallet');
  const paymentStatusLabelEl = document.getElementById('paymentStatusLabel');
  const orderErrorEl = document.getElementById('orderError');
  const buyPayerInfo = document.getElementById('buyPayerInfo');
  const buyCustomerNameInput = document.getElementById('buyCustomerName');
  const buyCustomerCpfInput = document.getElementById('buyCustomerCpf');
  const pixCpfInput = document.getElementById('pixCpf');
  const pixPhoneInput = document.getElementById('pixPhone');
  const pixKeyDisplay = document.getElementById('pixKey');
  const qrCodeImg = document.getElementById('qrCodeImg');
  const step3PixQr = document.getElementById('step3PixQr');
  const step3PixCopy = document.getElementById('step3PixCopy');
  const step3PixCopyBtn = document.getElementById('step3PixCopyBtn');
  const paymentTxHash = document.getElementById('paymentTxHash');
  const statusMessage = document.getElementById('statusMessage');
  const particlesContainer = document.getElementById('particles-container');
  const premiumTitleEl = document.getElementById('premiumTitle');
  const payCurrencyBtn = document.getElementById('payCurrency');
  const receiveCurrencyBtn = document.getElementById('receiveCurrency');
  const payDropdown = document.getElementById('payDropdown');
  const receiveDropdown = document.getElementById('receiveDropdown');
  const toggleButtons = document.querySelectorAll('.toggle-btn[data-action]');
  const walletGroup = document.getElementById('walletGroup');
  const sellInfo = document.getElementById('sellInfo');
  const paymentMethodsSection = document.getElementById('paymentMethodsSection');
  const mobileMenuToggle = document.querySelector('.mobile-menu-toggle');
  const primaryNav = document.getElementById('primaryNav');
  const navViewLinks = document.querySelectorAll('.nav-links a[data-view]');
  const developersView = document.getElementById('developersView');
  const marketsView = document.getElementById('marketsView');
  const pricingView = document.getElementById('pricingView');
  const marketFilterButtons = document.querySelectorAll('[data-market-filter]');
  const marketRows = document.querySelectorAll('.markets-row[data-market-category]');
  const marketActionButtons = document.querySelectorAll('[data-market-action]');

  function syncBuyPayerInfo() {
    const isBuyPix = state.action === 'buy' && selectedPaymentRail().method === 'pix';
    if (buyPayerInfo) buyPayerInfo.classList.toggle('hidden', !isBuyPix);
  }

  function collectBuyPixCustomer() {
    return {
      name: (buyCustomerNameInput?.value || '').trim(),
      cpf: digitsOnly(buyCustomerCpfInput?.value || '')
    };
  }

  function validateBuyPixCustomer() {
    const customer = collectBuyPixCustomer();
    if (!customer.name || customer.name.length < 3) {
      showUxMessage('payer_name_required', 'warning');
      buyCustomerNameInput?.focus();
      return null;
    }
    if (!customer.cpf) {
      showUxMessage('payer_cpf_required', 'warning');
      buyCustomerCpfInput?.focus();
      return null;
    }
    if (!isValidCPF(customer.cpf)) {
      showUxMessage('payer_cpf_invalid', 'warning');
      buyCustomerCpfInput?.focus();
      return null;
    }
    return customer;
  }

  function setSelectedSellNetwork(network) {
    const requested = normalizeSellNetwork(network);
    const enabledNetworks = normalizeSellNetworks(priceState.sellNetworks);
    state.sellNetwork = enabledNetworks.includes(requested) ? requested : enabledNetworks[0];
    priceState.sellNetwork = state.sellNetwork;
    updateSellDepositWallet();
  }

  function setPageView(view) {
    const isDevelopers = view === 'developers';
    const isMarkets = view === 'markets';
    const isPricing = view === 'pricing';
    document.body.classList.toggle('developers-mode', isDevelopers);
    document.body.classList.toggle('markets-mode', isMarkets);
    document.body.classList.toggle('pricing-mode', isPricing);
    if (developersView) developersView.classList.toggle('hidden', !isDevelopers);
    if (marketsView) marketsView.classList.toggle('hidden', !isMarkets);
    if (pricingView) pricingView.classList.toggle('hidden', !isPricing);
    navViewLinks.forEach(link => {
      const linkView = link.dataset.view || 'trade';
      link.classList.toggle('active', isDevelopers || isMarkets || isPricing ? linkView === view : linkView === 'trade');
    });
    if (isDevelopers || isMarkets || isPricing) {
      window.scrollTo({ top: 0, behavior: 'smooth' });
    }
  }

  function setMobileMenu(open) {
    if (!mobileMenuToggle || !primaryNav) return;
    mobileMenuToggle.classList.toggle('is-open', open);
    primaryNav.classList.toggle('is-open', open);
    mobileMenuToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
    mobileMenuToggle.setAttribute('aria-label', open ? 'Close navigation menu' : 'Open navigation menu');
  }

  if (mobileMenuToggle && primaryNav) {
    mobileMenuToggle.addEventListener('click', event => {
      event.stopPropagation();
      setMobileMenu(!primaryNav.classList.contains('is-open'));
    });

    document.addEventListener('click', event => {
      if (!primaryNav.classList.contains('is-open')) return;
      if (primaryNav.contains(event.target) || mobileMenuToggle.contains(event.target)) return;
      setMobileMenu(false);
    });

    document.addEventListener('keydown', event => {
      if (event.key === 'Escape') setMobileMenu(false);
    });
  }

  navViewLinks.forEach(link => {
    link.addEventListener('click', event => {
      const view = link.dataset.view || 'trade';
      setMobileMenu(false);
      if (view === 'developers') {
        event.preventDefault();
        history.replaceState(null, '', '#developers');
        setPageView('developers');
        return;
      }
      if (view === 'markets') {
        event.preventDefault();
        history.replaceState(null, '', '#markets');
        setPageView('markets');
        return;
      }
      if (view === 'pricing') {
        event.preventDefault();
        history.replaceState(null, '', '#pricing');
        setPageView('pricing');
        return;
      }
      event.preventDefault();
      history.replaceState(null, '', '#trade');
      setPageView('trade');
    });
  });

  if (window.location.hash === '#developers' || window.location.hash.startsWith('#api-')) {
    setPageView('developers');
  } else if (window.location.hash === '#markets') {
    setPageView('markets');
  } else if (window.location.hash === '#pricing') {
    setPageView('pricing');
  } else {
    setPageView('trade');
  }

  const devSidebarLinks = document.querySelectorAll('[data-dev-link]');
  const devPanels = document.querySelectorAll('.developer-panel[id]');

  if (devSidebarLinks.length && devPanels.length) {
    const setActiveDevLink = id => {
      devSidebarLinks.forEach(link => {
        link.classList.toggle('is-active', link.getAttribute('href') === `#${id}`);
      });
    };

    if ('IntersectionObserver' in window) {
      const devObserver = new IntersectionObserver(entries => {
        const visible = entries
          .filter(entry => entry.isIntersecting)
          .sort((a, b) => b.intersectionRatio - a.intersectionRatio)[0];
        if (visible) setActiveDevLink(visible.target.id);
      }, { root: null, rootMargin: '-96px 0px -55% 0px', threshold: [0.1, 0.25, 0.5, 0.75] });

      devPanels.forEach(panel => devObserver.observe(panel));
      setActiveDevLink(devPanels[0].id);
    }

    devSidebarLinks.forEach(link => {
      link.addEventListener('click', event => {
        const id = link.getAttribute('href').slice(1);
        const target = document.getElementById(id);
        if (target) {
          event.preventDefault();
          setActiveDevLink(id);
          target.scrollIntoView({ behavior: 'smooth', block: 'start' });
          history.replaceState(null, '', `#developers`);
        }
      });
    });
  }

  const devHeroCta = document.querySelector('.developers-actions a[href="#api-buy"]');
  if (devHeroCta) {
    devHeroCta.addEventListener('click', event => {
      const target = document.getElementById('api-buy');
      if (target) {
        event.preventDefault();
        devSidebarLinks.forEach(link => {
          link.classList.toggle('is-active', link.getAttribute('href') === '#api-buy');
        });
        target.scrollIntoView({ behavior: 'smooth', block: 'start' });
        history.replaceState(null, '', '#developers');
      }
    });
  }

  document.querySelectorAll('[data-copy-target]').forEach(button => {
    button.addEventListener('click', async () => {
      const codeWindow = button.closest('.code-window');
      const codeEl = codeWindow ? codeWindow.querySelector('pre code') : null;
      if (!codeEl) return;
      try {
        await navigator.clipboard.writeText(codeEl.textContent);
        const original = button.textContent;
        button.textContent = 'Copied';
        button.classList.add('is-copied');
        setTimeout(() => {
          button.textContent = original;
          button.classList.remove('is-copied');
        }, 1600);
      } catch (err) {
        console.warn('Copy failed', err);
      }
    });
  });

  marketFilterButtons.forEach(button => {
    button.addEventListener('click', () => {
      const filter = button.dataset.marketFilter || 'all';
      marketFilterButtons.forEach(item => item.classList.toggle('active', item === button));
      marketRows.forEach(row => {
        const categories = (row.dataset.marketCategory || '').split(/\s+/);
        row.classList.toggle('hidden', filter !== 'all' && !categories.includes(filter));
      });
    });
  });

  // Apply blur/shake animation to hero title text
  if (premiumTitleEl) {
    premiumTitleEl.classList.add('blur-shake');
  }

  /* Re-enable floating icon glow effect
  if (particlesContainer) {
    particlesContainer.innerHTML = ''; // reset if rerun
    const spawnParticles = (count = 18) => {
      for (let i = 0; i < count; i++) {
        const img = document.createElement('img');
        img.src = PARTICLE_ICON;
        img.className = 'particle';
        const left = Math.random() * 100;
        const xOffset = (Math.random() * 60 - 30).toFixed(1); // -30vw to +30vw drift
        const duration = (14 + Math.random() * 10).toFixed(1); // 14s – 24s
        const delay = (-Math.random() * duration).toFixed(1); // negative delays to desync
        const scale = (0.6 + Math.random() * 0.8).toFixed(2); // 0.6x – 1.4x size variance
        img.style.left = `${left}%`;
        img.style.setProperty('--x-offset', `${xOffset}vw`);
        img.style.animationDuration = `${duration}s`;
        img.style.animationDelay = `${delay}s`;
        img.style.transform = `scale(${scale})`;
        particlesContainer.appendChild(img);
      }
    };
    spawnParticles();
  }
  */

  function setOrderError(msg) {
    if (!orderErrorEl) return;
    if (msg) {
      showUxMessage(msg, 'error', { inlineEl: orderErrorEl });
    } else {
      orderErrorEl.textContent = '';
      orderErrorEl.className = 'ux-inline-message';
      orderErrorEl.style.display = 'none';
    }
  }

  function shortStatusMessage(status) {
    switch (status) {
      case 'aguardando_pix':
      case 'aguardando_credit_card':
        return UX_MESSAGES.webhook_pending;
      case 'pago_fiat':
      case 'pago_pix':
        return UX_MESSAGES.payment_identified;
      case 'enviado':
      case 'delivered':
      case 'confirmado':
      case 'completed':
        return UX_MESSAGES.payment_sent;
      case 'erro':
        return UX_MESSAGES.payment_failed;
      default:
        return status ? `Status: ${status}` : UX_MESSAGES.webhook_pending;
    }
  }

  function optionFor(code) {
    return [...CURRENCY_OPTIONS.fiat, ...CURRENCY_OPTIONS.crypto, ...SELL_RECEIVE_OPTIONS].find(item => item.code === code) || CURRENCY_OPTIONS.crypto[0];
  }

  function setSelectorButton(button, option) {
    if (!button || !option) return;
    const img = button.querySelector('img');
    const label = button.querySelector('span');
    if (img) {
      img.src = option.icon;
      img.alt = option.label;
    }
    if (label) label.textContent = option.label;
  }

  function renderCurrencyDropdown(dropdown, options, onSelect) {
    if (!dropdown) return;
    dropdown.innerHTML = options.map(option => `
      <div role="button" tabindex="0" data-currency="${option.code}">
        <img src="${option.icon}" alt="${option.label}" width="24" height="24" />
        <span>${option.label}</span>
      </div>
    `).join('');
    dropdown.querySelectorAll('[data-currency]').forEach(item => {
      const select = () => {
        const option = options.find(entry => entry.code === item.dataset.currency);
        if (option) onSelect(option);
        closeCurrencyDropdowns();
      };
      item.addEventListener('click', select);
      item.addEventListener('keydown', event => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          select();
        }
      });
    });
  }

  function closeCurrencyDropdowns() {
    payDropdown?.classList.add('hidden');
    receiveDropdown?.classList.add('hidden');
  }

  function updateRateLabel() {
    if (!rateText) return;
    const rateSource = state.action === 'sell' ? state.payCurrency : state.receiveCurrency;
    const pool = LIQUIDITY_POOLS[rateSource] || LIQUIDITY_POOLS.USDT;
    const rate = state.action === 'sell'
      ? getSellAssetBrlPrice(rateSource)
      : Number(pool?.price || state.exchangeRate || 0);
    if (!rate) {
      rateText.textContent = UX_MESSAGES.quote_unavailable;
      return;
    }
    rateText.textContent = `1 ${rateSource} ≈ ${rate.toLocaleString('pt-BR', { style: 'currency', currency: 'BRL' })}`;
  }

  function setReceiveCurrency(option) {
    if (state.action === 'sell' && option.code !== 'BRL') {
      option = SELL_RECEIVE_OPTIONS[0];
    }
    state.receiveCurrency = option.code;
    setSelectorButton(receiveCurrencyBtn, option);
    updateRateLabel();
    updateReceiveAmount();
    updateOrderSummaries();
  }

  function setPayCurrency(option) {
    state.payCurrency = option.code;
    setSelectorButton(payCurrencyBtn, option);
    updateRateLabel();
    updateReceiveAmount();
    updateOrderSummaries();
  }

  function syncTradeModeLabels() {
    const isSell = state.action === 'sell';
    if (payAmountLabel) payAmountLabel.textContent = isSell ? 'Sell' : 'Pay';
    if (receiveAmountLabel) receiveAmountLabel.textContent = 'Receive';
    if (payCurrencyBtn) payCurrencyBtn.setAttribute('aria-label', isSell ? 'Select crypto to sell' : 'Select currency to pay');
    if (receiveCurrencyBtn) receiveCurrencyBtn.setAttribute('aria-label', isSell ? 'Select fiat to receive' : 'Select asset to receive');
    if (receiveCurrencyBtn) {
      receiveCurrencyBtn.disabled = false;
      receiveCurrencyBtn.classList.toggle('locked', isSell);
      if (isSell) {
        setSelectorButton(receiveCurrencyBtn, optionFor('BRL'));
      }
    }
  }

  function syncTradeModeUI() {
    const isSell = state.action === 'sell';
    if (isSell) {
      state.payCurrency = 'USDT';
      state.receiveCurrency = 'BRL';
    } else {
      state.payCurrency = 'BRL';
      state.receiveCurrency = 'USDT';
    }
    toggleButtons.forEach(button => button.classList.toggle('active', button.dataset.action === state.action));
    if (walletGroup) walletGroup.classList.toggle('hidden', isSell);
    if (sellInfo) sellInfo.classList.toggle('hidden', !isSell);
    if (paymentMethodsSection) paymentMethodsSection.classList.toggle('hidden', isSell);
    syncTradeModeLabels();
    renderCurrencyDropdown(payDropdown, isSell ? CURRENCY_OPTIONS.crypto : CURRENCY_OPTIONS.fiat, setPayCurrency);
    renderCurrencyDropdown(receiveDropdown, isSell ? SELL_RECEIVE_OPTIONS : CURRENCY_OPTIONS.crypto, setReceiveCurrency);

    if (isSell) {
      setPayCurrency(optionFor('USDT'));
      setReceiveCurrency(optionFor('BRL'));
      setSelectedSellNetwork(state.sellNetwork || 'BSC');
    } else {
      setPayCurrency(optionFor('BRL'));
      setReceiveCurrency(optionFor('USDT'));
    }

    state.selectedPaymentMethod = null;
    paymentMethodButtons.forEach(button => button.classList.remove('selected'));
    syncBuyPayerInfo();
    updateStep(1);
    syncTradeModeLabels();
    if (continueBtn) continueBtn.textContent = isSell ? 'Sell Now' : 'Buy Now';
  }

  function resetCheckoutFlow() {
    state.payAmount = 0;
    state.totalPayAmount = 0;
    state.platformFee = 0;
    state.selectedPaymentMethod = null;
    state.cardCheckoutStep = 1;
    state.walletAddress = '';
    state.connected = false;
    currentBuyId = null;
    currentBuyAccessToken = null;
    currentBuyTxHash = null;
    currentSellId = null;
    currentSellAccessToken = null;
    if (buySse) {
      buySse.close();
      buySse = null;
    }
    if (buyPoll) {
      clearInterval(buyPoll);
      buyPoll = null;
    }
    if (sellSse) {
      sellSse.close();
      sellSse = null;
    }
    if (sellPoll) {
      clearInterval(sellPoll);
      sellPoll = null;
    }

    if (payAmountInput) payAmountInput.value = '';
    if (receiveAmountInput) receiveAmountInput.value = '';
    if (walletAddressInput) walletAddressInput.value = '';
    if (buyCustomerNameInput) buyCustomerNameInput.value = '';
    if (buyCustomerCpfInput) buyCustomerCpfInput.value = '';
    if (step3PixQr) step3PixQr.src = '/images/qrcode.png';
    if (step3PixCopy) step3PixCopy.value = '';
    if (sellDepositBlock) sellDepositBlock.classList.add('hidden');
    if (sellDepositAddressInput) sellDepositAddressInput.value = '';
    setCardCheckoutStep(1);
    updatePaymentTxHash('');
    paymentMethodButtons.forEach(button => {
      button.classList.remove('selected');
      button.style.borderColor = '';
    });
    updateStep3PaymentPreview(false);
    updateStep(1);
  }

  function startBuyStream(buyId) {
    if (buySse) buySse.close();
    startBuyStatusPolling();
    try {
      const streamPath = currentBuyAccessToken
        ? `/api/buy/${encodeURIComponent(buyId)}/stream?accessToken=${encodeURIComponent(currentBuyAccessToken)}`
        : `/api/buy/${encodeURIComponent(buyId)}/stream`;
      buySse = new EventSource(`${API_BASE}${streamPath}`);
      buySse.onmessage = (ev) => {
        try {
          const data = JSON.parse(ev.data);
          updateFinalPaymentStatus(data.status);
          if (data.txHash) updatePaymentTxHash(data.txHash);
          if (orderStatusEl) orderStatusEl.textContent = data.status || '—';
          if (statusMessage) statusMessage.textContent = shortStatusMessage(data.status);
          if (isPaymentIdentifiedStatus(data.status) && state.currentStep === 3) updateStep(5);
          if (['enviado', 'delivered', 'confirmado'].includes(data.status)) showUxMessage('payment_sent', 'success');
          if (data.status === 'erro') showUxMessage('payment_failed', 'error');
        } catch (e) { /* ignore */ }
      };
      buySse.onerror = () => buySse && buySse.close();
    } catch (e) {
      console.warn('SSE buy error', e);
    }
  }

  function startBuyStatusPolling() {
    if (buyPoll) clearInterval(buyPoll);
    buyPoll = setInterval(async () => {
      const data = await refreshBuyStatus().catch(() => null);
      if (!data?.status) return;
      if (isPaymentIdentifiedStatus(data.status) && state.currentStep === 3) updateStep(5);
      if (['enviado', 'delivered', 'confirmado', 'erro'].includes(String(data.status).toLowerCase())) {
        clearInterval(buyPoll);
        buyPoll = null;
      }
    }, 3000);
  }

  function updatePaymentTxHash(txHash) {
    const cleanHash = typeof txHash === 'string' ? txHash.trim() : '';
    currentBuyTxHash = cleanHash || null;
    if (!paymentTxHash) return;
    if (!cleanHash) {
      paymentTxHash.textContent = 'Aguardando envio on-chain';
      return;
    }
    const href = /^0x[a-fA-F0-9]{64}$/.test(cleanHash) ? `https://bscscan.com/tx/${cleanHash}` : '';
    if (!href) {
      paymentTxHash.textContent = cleanHash;
      return;
    }
    paymentTxHash.innerHTML = `<a class="tx-hash-link" href="${href}" target="_blank" rel="noopener noreferrer">${shortenTxHash(cleanHash)}</a>`;
  }

  function shortenTxHash(txHash) {
    return txHash && txHash.length > 18 ? `${txHash.slice(0, 10)}...${txHash.slice(-8)}` : txHash;
  }

  function isPaymentIdentifiedStatus(status) {
    const normalized = String(status || '').toLowerCase();
    return ['pago', 'paid', 'pago_fiat', 'pago_pix', 'enviado', 'delivered', 'confirmado', 'confirmed', 'concluido', 'concluida'].some(value => normalized.includes(value));
  }

  async function refreshBuyStatus() {
    if (!currentBuyId || !currentBuyAccessToken) return null;
    const res = await fetch(`${API_BASE}/api/buy/${encodeURIComponent(currentBuyId)}?accessToken=${encodeURIComponent(currentBuyAccessToken)}`, { cache: 'no-store' });
    if (!res.ok) return null;
    const data = await res.json();
    if (orderStatusEl) orderStatusEl.textContent = data.status || 'aguardando_pix';
    updateFinalPaymentStatus(data.status);
    updatePaymentTxHash(data.txHashOut || data.tx_hash_out || data.txHash || '');
    return data;
  }

  function sellStatusMessage(status) {
    const normalized = String(status || '').toLowerCase();
    if (normalized.includes('aguardando_validacao')) return 'Deposito recebido. Aguardando validacao.';
    if (normalized.includes('aguardando_deposito')) return 'Aguardando deposito USDT.';
    if (normalized === 'pago' || normalized.includes('processando_payout')) return 'PIX em processamento.';
    if (normalized.includes('concluida') || normalized.includes('concluido')) return 'PIX enviado.';
    if (normalized.includes('erro')) return 'Falha no payout PIX.';
    if (normalized.includes('expirada')) return 'Ordem expirada.';
    return status ? `Status: ${status}` : 'Aguardando deposito USDT.';
  }

  function isSellDepositIdentifiedStatus(status) {
    const normalized = String(status || '').toLowerCase();
    if (!normalized || normalized.includes('aguardando_deposito') || normalized.includes('expirada') || normalized.includes('erro')) return false;
    return ['aguardando_validacao', 'deposito_recebido', 'deposit_received', 'pago', 'paid', 'processando_payout', 'concluido', 'concluida'].some(value => normalized.includes(value));
  }

  function setSellPaymentScreenComplete(isComplete) {
    const paymentInfoSection = document.getElementById('paymentInfoSection');
    if (!paymentInfoSection) return;
    const isSellPending = state.action === 'sell' && !isComplete;
    paymentInfoSection.classList.toggle('sell-deposit-pending', isSellPending);
    if (confirmPaymentBtn && state.action === 'sell' && state.currentStep === 5) {
      confirmPaymentBtn.textContent = isComplete ? 'Ver Scan' : 'Confirm';
    }
  }

  function applySellStatus(data = {}) {
    const status = data.status || 'aguardando_deposito';
    if (orderStatusEl) orderStatusEl.textContent = status;
    if (paymentStatusLabelEl) paymentStatusLabelEl.textContent = sellStatusMessage(status);
    if (statusMessage) statusMessage.textContent = '';
    setSellPaymentScreenComplete(isSellDepositIdentifiedStatus(status));
    const tx = data.depositTx || data.deposit_tx || data.txHash || data.tx_hash || '';
    if (tx) updatePaymentTxHash(tx);
    const depositAmount = data.depositAmount || data.deposit_amount;
    if (depositAmount && paymentBtcAmountEl) {
      paymentBtcAmountEl.textContent = `${Number(depositAmount).toFixed(6)} USDT`;
    }
    const normalized = String(status).toLowerCase();
    if (normalized.includes('concluida') || normalized.includes('concluido')) {
      showUxMessage('PIX enviado.', 'success');
      if (sellPoll) {
        clearInterval(sellPoll);
        sellPoll = null;
      }
      if (sellSse) {
        sellSse.close();
        sellSse = null;
      }
    }
    if (normalized.includes('erro')) {
      showUxMessage(data.error || 'Falha no payout PIX.', 'error');
    }
  }

  async function refreshSellStatus() {
    if (!currentSellId || !currentSellAccessToken) return null;
    const res = await fetch(`${API_BASE}/api/order/${encodeURIComponent(currentSellId)}?accessToken=${encodeURIComponent(currentSellAccessToken)}`, { cache: 'no-store' });
    if (!res.ok) return null;
    const data = await res.json();
    applySellStatus(data);
    return data;
  }

  function startSellStatusPolling() {
    if (sellPoll) clearInterval(sellPoll);
    sellPoll = setInterval(async () => {
      const data = await refreshSellStatus().catch(() => null);
      const status = String(data?.status || '').toLowerCase();
      if (status.includes('concluida') || status.includes('concluido') || status.includes('erro') || status.includes('expirada')) {
        clearInterval(sellPoll);
        sellPoll = null;
      }
    }, 3000);
  }

  function startSellStream(orderId) {
    if (sellSse) sellSse.close();
    startSellStatusPolling();
    try {
      const streamPath = currentSellAccessToken
        ? `/api/order/${encodeURIComponent(orderId)}/stream?accessToken=${encodeURIComponent(currentSellAccessToken)}`
        : `/api/order/${encodeURIComponent(orderId)}/stream`;
      sellSse = new EventSource(`${API_BASE}${streamPath}`);
      sellSse.onmessage = (ev) => {
        try {
          applySellStatus(JSON.parse(ev.data));
        } catch (e) { /* ignore */ }
      };
      sellSse.onerror = () => sellSse && sellSse.close();
    } catch (e) {
      console.warn('SSE sell error', e);
    }
  }

  async function refreshOrderQuote() {
    const amount = parseFloat(payAmountInput?.value || '0');
    if (!amount || amount <= 0) {
      showUxMessage('invalid_amount', 'warning');
      return null;
    }

    const payload = {
      mode: state.action,
      asset: 'USDT',
      amountFiat: state.action === 'sell' ? amount : amount,
      amountBRL: state.action === 'buy' ? convertFiatToBrl(amount, state.payCurrency) : 0,
      fiatCurrency: state.action === 'buy' ? state.payCurrency : 'BRL',
      paymentMethod: 'pix'
    };

    try {
      const resp = await fetch(`${API_BASE}/api/quote`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      });
      if (!resp.ok) {
        const data = await resp.json().catch(() => ({}));
        showUxMessage(data.error || 'quote_unavailable', 'warning');
        return null;
      }
      const data = await resp.json();
      state.platformFee = Number(data.feeFiat || data.spreadFiat || 0) || 0;
      state.totalPayAmount = Number(data.totalFiat || data.amountFiat || amount) || amount;
      if (data.rate) {
        if (state.action === 'sell') {
          priceState.rates.SELLUSDTBRL = Number(data.rate) || priceState.rates.SELLUSDTBRL;
        } else {
          state.exchangeRate = Number(data.rate) || state.exchangeRate;
          LIQUIDITY_POOLS.USDT.price = state.exchangeRate;
        }
        updateRateLabel();
      }
      if (receiveAmountInput) {
        if (state.action === 'sell' && data.payoutFiat) {
          receiveAmountInput.value = Number(data.payoutFiat).toFixed(2);
        } else if (data.cryptoAmount) {
          receiveAmountInput.value = Number(data.cryptoAmount).toFixed(6);
        }
      }
      updateOrderSummaries();
      return data;
    } catch (err) {
      console.error(err);
      showUxMessage('quote_unavailable', 'warning');
      return null;
    }
  }

  async function createBuyOrder(options = {}) {
    if (currentBuyId) return { buyId: currentBuyId, accessToken: currentBuyAccessToken };
    if (buyOrderPromise) return buyOrderPromise;

    buyOrderPromise = (async () => {
      setOrderError('');
      const amount = parseFloat(payAmountInput?.value || '0');
      const amountBRL = convertFiatToBrl(amount, state.payCurrency);
      const destAddr = walletAddressInput?.value?.trim();
      const paymentMethod = options.paymentMethod || selectedPaymentRail().paymentMethod || 'pix';
      const buyPixCustomer = paymentMethod === 'pix' ? validateBuyPixCustomer() : null;

      if (!amount || amount <= 0) return setOrderError('invalid_amount');
      if (!destAddr || !validateWalletAddress(destAddr)) return setOrderError('invalid_wallet');
      if (state.receiveCurrency !== 'USDT') return setOrderError('Asset nao suportado nesta fase. Use USDT para finalizar a compra.');
      if (paymentMethod === 'pix' && !buyPixCustomer) return null;

      try {
        const payload = {
          amountBRL,
          amountFiat: amount,
          fiatCurrency: state.payCurrency,
          paymentMethod,
          asset: 'USDT',
          address: destAddr
        };
        if (paymentMethod === 'pix') {
          payload.customer = buyPixCustomer;
          payload.cpf = buyPixCustomer.cpf;
          payload.customerName = buyPixCustomer.name;
        }
        if (paymentMethod === 'credit_card') {
          Object.assign(payload, {
            paymentToken: options.paymentToken,
            cardBrand: options.cardBrand,
            installments: options.installments || 1,
            customer: options.customer,
            billingAddress: options.billingAddress,
            card: {
              paymentToken: options.paymentToken,
              brand: options.cardBrand,
              installments: options.installments || 1,
              billingAddress: options.billingAddress
            }
          });
        }
        const resp = await fetch(`${API_BASE}/api/buy`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });

        if (!resp.ok) {
          const data = await resp.json().catch(() => ({}));
          const msg = data.error || (resp.status === 429 ? 'order_limit' : 'backend_unavailable');
          setOrderError(msg);
          return null;
        }

        const data = await resp.json();
        currentBuyId = data.buyId || data.id;
        currentBuyAccessToken = data.accessToken || data.customerAccessToken || null;
        updatePaymentTxHash(data.txHash || data.txHashOut || data.payment?.txHash || '');
        state.totalPayAmount = Number(data.totalFiat || data.amountFiat || amount) || amount;
        state.platformFee = Number(data.feeFiat || 0) || 0;
        if (data.cryptoAmount && receiveAmountInput) {
          receiveAmountInput.value = Number(data.cryptoAmount).toFixed(6);
        }
        updateOrderSummaries();
        if (orderIdEl) orderIdEl.textContent = currentBuyId || '-';
        if (orderStatusEl) orderStatusEl.textContent = data.status || (paymentMethod === 'credit_card' ? 'aguardando_credit_card' : 'aguardando_pix');
        updateFinalPaymentStatus(data.status);
        if (pixKeyDisplay) pixKeyDisplay.textContent = data.pixKey || data.payment?.pixKey || 'chavepix@nexswap.com';
        const qrUrl = normalizeQrImageSrc(data.qrCodeUrl || data.payment?.qrCodeUrl);
        const pixCopy = data.pixCopiaECola || data.payment?.pixCopiaECola || data.payment?.qrcode || '';
        if (qrCodeImg && qrUrl) qrCodeImg.src = qrUrl;
        if (step3PixQr && qrUrl) step3PixQr.src = qrUrl;
        if (step3PixCopy) step3PixCopy.value = pixCopy;
        if (statusMessage) statusMessage.textContent = paymentMethod === 'credit_card' ? 'Aguardando confirmacao da Efi.' : UX_MESSAGES.webhook_pending;
        showUxMessage('webhook_pending', 'info');
        if (currentBuyId) startBuyStream(currentBuyId);
        return data;
      } catch (err) {
        console.error(err);
        setOrderError('backend_unavailable');
        return null;
      } finally {
        buyOrderPromise = null;
      }
    })();

    return buyOrderPromise;
  }

  async function createCreditCardBuyOrder() {
    const checkout = collectCardCheckoutInput();
    if (!validateCardCheckoutInput(checkout)) {
      showUxMessage('card_fields_required', 'warning');
      return null;
    }
    if (!checkout.rail.cardBrand) {
      showUxMessage('payment_method_required', 'warning');
      return null;
    }
    try {
      if (continueBtn) {
        continueBtn.disabled = true;
        continueBtn.textContent = 'Tokenizando...';
      }
      const paymentToken = await generateEfiPaymentToken(checkout);
      if (continueBtn) continueBtn.textContent = 'Criando cobranca...';
      const data = await createBuyOrder({
        paymentMethod: 'credit_card',
        paymentToken,
        cardBrand: checkout.rail.cardBrand,
        installments: 1,
        customer: checkout.customer,
        billingAddress: checkout.billingAddress
      });
      if (!data) return null;
      showUxMessage('webhook_pending', 'info');
      updateStep(5);
      return data;
    } catch (error) {
      console.error(error);
      showUxMessage(error?.message || 'card_token_unavailable', 'error');
      return null;
    } finally {
      if (continueBtn) {
        continueBtn.disabled = false;
        if (state.currentStep === 3) setCardCheckoutStep(state.cardCheckoutStep);
      }
    }
  }

  async function createSellOrder() {
    setOrderError('');
    const amountUSDT = parseFloat(payAmountInput?.value || '0');
    const cpf = pixCpfInput?.value?.replace(/\D/g, '') || '';
    const phone = pixPhoneInput?.value?.replace(/\D/g, '') || '';

    if (!amountUSDT || amountUSDT <= 0) {
      showUxMessage('invalid_amount', 'warning');
      return null;
    }
    if (!cpf || !phone) {
      showUxMessage('Informe CPF e chave PIX.', 'warning');
      return null;
    }

    try {
      const selectedNetwork = normalizeSellNetwork(state.sellNetwork);
      const payoutBRL = parseFloat(receiveAmountInput?.value || '0') || 0;
      const resp = await fetch(`${API_BASE}/api/order`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          amountUSDT,
          amountBRL: payoutBRL,
          asset: 'USDT',
          network: selectedNetwork,
          pixCpf: cpf,
          pixPhone: phone
        })
      });

      if (!resp.ok) {
        const data = await resp.json().catch(() => ({}));
        showUxMessage(data.error || 'backend_unavailable', 'error');
        return null;
      }

      const data = await resp.json();
      currentSellId = data.orderId || data.id || null;
      currentSellAccessToken = data.accessToken || null;
      priceState.sellWallet = data.depositAddress || data.address || priceState.sellWallet;
      setSelectedSellNetwork(data.network || selectedNetwork);
      state.totalPayAmount = amountUSDT;
      state.platformFee = Number(data.spreadBRL || data.feeBRL || 0) || 0;
      const pixReceiveBRL = Number(data.payoutBRL || payoutBRL || receiveAmountInput?.value || 0);
      if (receiveAmountInput && pixReceiveBRL > 0) receiveAmountInput.value = pixReceiveBRL.toFixed(2);
      if (orderIdEl) orderIdEl.textContent = currentSellId || '-';
      if (orderStatusEl) orderStatusEl.textContent = data.status || 'aguardando_deposito';
      if (depositAddressEl) depositAddressEl.textContent = data.depositAddress || data.address || priceState.sellWallet;
      if (sellDepositAddressInput) sellDepositAddressInput.value = data.depositAddress || data.address || priceState.sellWallet;
      if (paymentBtcAmountEl) paymentBtcAmountEl.textContent = `${amountUSDT.toFixed(6)} USDT`;
      if (paymentWalletEl) paymentWalletEl.textContent = pixReceiveBRL > 0 ? `R$ ${pixReceiveBRL.toFixed(2).replace('.', ',')}` : getReceiveDisplayValue();
      if (paymentStatusLabelEl) paymentStatusLabelEl.textContent = 'Aguardando deposito';
      if (sellDepositBlock) sellDepositBlock.classList.remove('hidden');
      updatePaymentTxHash(data.depositTx || data.txHash || '');
      updateOrderSummaries();
      applySellStatus(data);
      updateStep(5);
      setSellPaymentScreenComplete(isSellDepositIdentifiedStatus(data.status));
      if (currentSellId) startSellStream(currentSellId);
      return data;
    } catch (err) {
      console.error(err);
      showUxMessage('backend_unavailable', 'error');
      return null;
    }
  }

  // --- Initial Setup ---

  async function fetchJson(url) {
    const res = await fetch(url, { cache: 'no-store' });
    if (!res.ok) throw new Error(`${url} ${res.status}`);
    return res.json();
  }

  async function fetchPriceWithFallback() {
    const attempts = [
      async () => normalizePriceSnapshot(await fetchJson(`${API_BASE}/api/price`), 'backend'),
      async () => normalizePriceSnapshot(await fetchJson(`${API_BASE}/rates`), 'backend'),
      async () => normalizePriceSnapshot(await fetchJson('https://api.coingecko.com/api/v3/simple/price?ids=tether,bitcoin&vs_currencies=brl,usd,eur'), 'coingecko'),
      async () => {
        const [usdtBrl, btcUsdt, eurUsdt] = await Promise.all([
          fetchJson('https://api.binance.com/api/v3/ticker/price?symbol=USDTBRL'),
          fetchJson('https://api.binance.com/api/v3/ticker/price?symbol=BTCUSDT'),
          fetchJson('https://api.binance.com/api/v3/ticker/price?symbol=EURUSDT')
        ]);
        return normalizePriceSnapshot({
          brl: readPositiveNumber(usdtBrl?.price),
          usd: 1,
          eurusd: readPositiveNumber(eurUsdt?.price),
          btcusdt: readPositiveNumber(btcUsdt?.price)
        }, 'binance');
      }
    ];

    let lastError;
    for (const attempt of attempts) {
      try {
        const snapshot = await attempt();
        if (snapshot?.rates?.USDTBRL > 0) return snapshot;
      } catch (err) {
        lastError = err;
      }
    }
    throw lastError || new Error('cotacao indisponivel');
  }

  // Fetch prices and update every UI surface from one snapshot.
  try {
    const snapshot = await fetchPriceWithFallback();
    applyPriceSnapshot(snapshot);
    updateRateLabel();
    syncMarketsFromPriceState();
    console.log("Rates fetched:", priceState);
    updateReceiveAmount();
    updateOrderSummaries();
    if (rateSpinner) rateSpinner.classList.add('hidden');
  } catch (err) {
    console.error("Erro ao buscar o preco do USDT:", err);
    rateText.textContent = UX_MESSAGES.quote_unavailable;
    showUxMessage('quote_unavailable', 'warning');
    state.exchangeRate = 0;
    LIQUIDITY_POOLS.USDT.price = 0;
    if (rateSpinner) rateSpinner.classList.add('hidden');
  }
  // Set initial step and update UI
  updateStep(state.currentStep); // Start at Step 1

  // --- Event Listeners ---

  syncTradeModeUI();
  setSelectedSellNetwork(state.sellNetwork);

  sellNetworkOptions.forEach(button => {
      button.addEventListener('click', () => {
          setSelectedSellNetwork(button.dataset.network);
      });
  });

  if (sellNetworkSelect) {
      sellNetworkSelect.addEventListener('change', () => {
          setSelectedSellNetwork(sellNetworkSelect.value);
      });
  }

  if (payCurrencyBtn && payDropdown) {
      payCurrencyBtn.addEventListener('click', (event) => {
          event.stopPropagation();
          receiveDropdown?.classList.add('hidden');
          payDropdown.classList.toggle('hidden');
      });
  }

  if (receiveCurrencyBtn && receiveDropdown) {
      receiveCurrencyBtn.addEventListener('click', (event) => {
          event.stopPropagation();
          if (state.action === 'sell') {
              setReceiveCurrency(optionFor('BRL'));
              payDropdown?.classList.add('hidden');
              receiveDropdown.classList.toggle('hidden');
              return;
          }
          payDropdown?.classList.add('hidden');
          receiveDropdown.classList.toggle('hidden');
      });
  }

  document.addEventListener('click', (event) => {
      if (!event.target.closest('.currency-selector') && !event.target.closest('.dropdown-options')) {
          closeCurrencyDropdowns();
      }
  });

  toggleButtons.forEach(button => {
      button.addEventListener('click', () => {
          if (button.dataset.action === state.action) return;
          state.action = button.dataset.action;
          state.payAmount = 0;
          state.totalPayAmount = 0;
          state.platformFee = 0;
          state.walletAddress = '';
          if (payAmountInput) payAmountInput.value = '';
          if (receiveAmountInput) receiveAmountInput.value = '';
          if (walletAddressInput) walletAddressInput.value = '';
          syncTradeModeUI();
      });
  });

  marketActionButtons.forEach(button => {
      button.addEventListener('click', () => {
          const row = button.closest('.markets-row');
          const assetCode = row?.querySelector('.market-asset strong')?.textContent?.trim() || 'USDT';
          const option = optionFor(assetCode);
          const action = button.dataset.marketAction === 'sell' ? 'sell' : 'buy';

          state.action = action;
          state.payAmount = 0;
          state.totalPayAmount = 0;
          state.platformFee = 0;
          state.walletAddress = '';
          if (payAmountInput) payAmountInput.value = '';
          if (receiveAmountInput) receiveAmountInput.value = '';
          if (walletAddressInput) walletAddressInput.value = '';

          syncTradeModeUI();
          if (action === 'buy') {
              setReceiveCurrency(option);
          } else {
              setPayCurrency(option);
              setReceiveCurrency(optionFor('BRL'));
          }
          history.replaceState(null, '', '#trade');
          setPageView('trade');
          document.getElementById('app')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
      });
  });

  // Listen for input on the pay amount field to update the receive amount
  if (payAmountInput) {
       payAmountInput.addEventListener('input', () => {
           updateReceiveAmount();
           if (state.currentStep > 1) {
               state.payAmount = parseFloat(payAmountInput.value) || 0;
               updateOrderSummaries();
           }
       });
  } else {
       console.warn("Element with id 'payAmount' not found.");
  }

  // Listen for clicks on payment method buttons
  if (paymentMethodButtons.length > 0) {
       paymentMethodButtons.forEach(btn => {
           btn.addEventListener('click', () => {
               // Remove 'selected' class from all buttons
               paymentMethodButtons.forEach(b => b.classList.remove('selected'));
               // Add 'selected' class to the clicked button
               btn.classList.add('selected');
               // Store the selected method element in state
               state.selectedPaymentMethod = btn;
               state.cardCheckoutStep = 1;
               syncBuyPayerInfo();
               updateStep3PaymentPreview(false);
               console.log("Selected payment method:", btn.dataset.method);
           });
       });
  } else {
       console.warn("Elements with class 'payment-method' not found.");
  }

  const maskedInputs = [
      ['cardNumber', value => digitsOnly(value).slice(0, 19).replace(/(.{4})/g, '$1 ').trim()],
      ['cardExpiry', value => {
          const digits = digitsOnly(value).slice(0, 4);
          return digits.length > 2 ? `${digits.slice(0, 2)}/${digits.slice(2)}` : digits;
      }],
      ['cardCvv', value => digitsOnly(value).slice(0, 4)],
      ['cardCpf', value => {
          return formatCPF(value);
      }],
      ['buyCustomerCpf', value => formatCPF(value)],
      ['cardPhone', value => digitsOnly(value).slice(0, 11)],
      ['billingZipcode', value => digitsOnly(value).slice(0, 8)],
      ['billingState', value => String(value || '').replace(/[^a-zA-Z]/g, '').slice(0, 2).toUpperCase()]
  ];
  maskedInputs.forEach(([id, formatter]) => {
      const input = document.getElementById(id);
      if (!input) return;
      input.addEventListener('input', () => {
          input.value = formatter(input.value);
      });
  });

  if (step3BypassBtn) {
      step3BypassBtn.addEventListener('click', async (event) => {
          event.preventDefault();
          event.stopPropagation();
          if (isLocalDevBypassEnabled()) {
              if (state.currentStep !== 3) return;
              if (!state.selectedPaymentMethod) {
                  state.selectedPaymentMethod = document.querySelector('.payment-method[data-method="pix"]');
              }
              if (orderStatusEl) orderStatusEl.textContent = 'dev_bypass_confirmed';
              if (statusMessage) statusMessage.textContent = 'Bypass local: confirmação liberada para revisar a UX.';
              updateStep(5);
              updatePaymentStatusLabel('confirmed');
              if (statusMessage) statusMessage.textContent = '';
              return;
          }
          if (state.currentStep !== 3 || state.selectedPaymentMethod?.dataset?.method !== 'pix') return;
          const data = await refreshBuyStatus();
          if (isPaymentIdentifiedStatus(data?.status)) updateStep(5);
      });
  }

  if (step3ConfirmPaymentBtn) {
      step3ConfirmPaymentBtn.addEventListener('click', async () => {
          if (state.currentStep !== 3 || state.selectedPaymentMethod?.dataset?.method !== 'pix') return;
          step3ConfirmPaymentBtn.disabled = true;
          step3ConfirmPaymentBtn.textContent = 'Checking...';
          try {
              const data = await refreshBuyStatus();
              if (isPaymentIdentifiedStatus(data?.status)) {
                  updateStep(5);
                  return;
              }
              if (statusMessage) statusMessage.textContent = UX_MESSAGES.webhook_pending;
              showUxMessage('webhook_pending', 'info');
          } finally {
              step3ConfirmPaymentBtn.disabled = false;
              step3ConfirmPaymentBtn.textContent = 'Confirm Payment';
          }
      });
  }

  if (step3PixCopyBtn && step3PixCopy) {
      step3PixCopyBtn.addEventListener('click', async () => {
          const value = step3PixCopy.value || '';
          if (!value) return;
          try {
              if (navigator.clipboard?.writeText) {
                  await navigator.clipboard.writeText(value);
              } else {
                  step3PixCopy.select();
                  document.execCommand('copy');
                  step3PixCopy.blur();
              }
              step3PixCopyBtn.classList.add('copied');
              step3PixCopyBtn.setAttribute('aria-label', 'Pix copiado');
              setTimeout(() => {
                  step3PixCopyBtn.classList.remove('copied');
                  step3PixCopyBtn.setAttribute('aria-label', 'Copiar Pix copia e cola');
              }, 1300);
          } catch (error) {
              console.warn('Falha ao copiar Pix', error);
          }
      });
  }

  if (sellDepositCopyBtn) {
      sellDepositCopyBtn.addEventListener('click', async () => {
          const value = document.getElementById('sellDepositWallet')?.textContent?.trim() || sellDepositAddressInput?.value || '';
          if (!value) return;
          try {
              if (navigator.clipboard?.writeText) {
                  await navigator.clipboard.writeText(value);
              } else {
                  const temp = document.createElement('textarea');
                  temp.value = value;
                  temp.setAttribute('readonly', '');
                  temp.style.position = 'fixed';
                  temp.style.opacity = '0';
                  document.body.appendChild(temp);
                  temp.select();
                  document.execCommand('copy');
                  temp.remove();
              }
              sellDepositCopyBtn.classList.add('copied');
              setTimeout(() => sellDepositCopyBtn.classList.remove('copied'), 1300);
          } catch (error) {
              console.warn('Falha ao copiar endereco de deposito', error);
          }
      });
  }

  if (confirmPaymentBtn) {
      confirmPaymentBtn.addEventListener('click', async () => {
          const isSellPendingDeposit = state.action === 'sell'
              && state.currentStep === 5
              && document.getElementById('paymentInfoSection')?.classList.contains('sell-deposit-pending');
          if (isSellPendingDeposit) {
              const data = await refreshSellStatus().catch(() => null);
              if (!isSellDepositIdentifiedStatus(data?.status)) {
                  showUxMessage('Aguardando deposito USDT.', 'warning');
              }
              return;
          }
          resetCheckoutFlow();
      });
  }

  // Listen for clicks on the 'Continue' button
  if (continueBtn) {
      continueBtn.addEventListener('click', async () => {
          switch (state.currentStep) {
              case 1: // Amount Input Step
                  const payAmount = parseFloat(payAmountInput.value);
                  if (isNaN(payAmount) || payAmount <= 0) {
                      showUxMessage('invalid_amount', 'warning');
                      return; // Stay on this step if validation fails
                  }
                  state.payAmount = payAmount; // Store validated amount in state
                  state.totalPayAmount = 0;
                  state.platformFee = 0;
                  updateReceiveAmount();
                  updateOrderSummaries();
                  if (!await refreshOrderQuote()) return;
                  updateStep(2); // Move to Wallet Step
                  break;

              case 2: // Wallet Input/Connect Step
                  if (state.action === 'sell') {
                      const sellOrder = await createSellOrder();
                      if (sellOrder) updateStep(5);
                      return;
                  }
                  // Validate the wallet address entered by the user.
                  const wallet = walletAddressInput.value.trim();
                  if (!wallet || !validateWalletAddress(wallet)) {
                       showUxMessage('invalid_wallet', 'warning');
                       // state.connected = false; // Optional: reset connected state if address becomes invalid
                       return; // Stay on step 2 if validation fails
                  }
                  state.walletAddress = wallet; // Store validated address in state
                  if (!state.selectedPaymentMethod) {
                      showUxMessage('payment_method_required', 'warning');
                      return;
                  }
                  if (selectedPaymentRail().method === 'pix' && !validateBuyPixCustomer()) {
                      return;
                  }
                  updateStep(3); // Move to Payment Method Step
                  if (state.selectedPaymentMethod.dataset.method === 'pix') {
                      void createBuyOrder();
                  }
                  break;

              case 3: // Payment Method Step
                  if (!state.selectedPaymentMethod) {
                      showUxMessage('payment_method_required', 'warning');
                      return; // Stay on step 3 if no method is selected
                  }
                  // No data to store in state here, selection is already in state.selectedPaymentMethod
                  if (selectedPaymentRail().paymentMethod === 'credit_card') {
                      if (advanceCreditCardCheckout()) await createCreditCardBuyOrder();
                      return;
                  }
                  if (state.selectedPaymentMethod.dataset.method !== 'pix') {
                      updateStep3PaymentPreview(true);
                      return;
                  }
                  return;

              case 5: // Confirmed payment Step
                  resetCheckoutFlow();
                  break;

              default:
                  console.error("Unknown step:", state.currentStep);
                  // Optionally reset to step 1
                  updateStep(1);
                  break;
          }
      });
  } else {
       console.warn("Element with id 'continueBtn' not found.");
  }

  // Optional: Add listeners for Buy/Sell toggle if it exists and controls state.action
  // Example (assuming buttons with classes 'action-buy' and 'action-sell'):
  // document.querySelectorAll('.action-toggle button').forEach(button => {
  //     button.addEventListener('click', () => {
  //          // Update state.action
  //          state.action = button.dataset.action; // Needs data-action="buy" or data-action="sell"
  //          // Update UI to show which is active
  //          document.querySelectorAll('.action-toggle button').forEach(btn => btn.classList.remove('active'));
  //          button.classList.add('active');
  //          console.log("Action set to:", state.action);
  //          // Maybe reset steps or update UI based on buy/sell
  //          // If switching action resets flow: updateStep(1);
  //     });
  // });

});

// --- Helper function for visual feedback on payment method selection (from first block) ---
// This logic is already integrated into the paymentMethodButtons click listener above,
// but you could keep a separate function if you call it from elsewhere.
// The integrated code adds/removes the 'selected' class and resets border style.

// ---------- Animated blue shader background ----------
(function initBlueShaderBackground() {
  const canvas = document.getElementById('canvas') || document.getElementById('bgShader');
  if (!canvas) return;

  const externalSource = document.getElementById('blueShaderSource');

  const gl = canvas.getContext('webgl2', { premultipliedAlpha: false });
  if (!gl) {
    canvas.style.display = 'none';
    return;
  }

  const vertexSrc = `#version 300 es
  in vec2 position;
  void main() { gl_Position = vec4(position, 0.0, 1.0); }`;

  const fragmentSrc = externalSource ? externalSource.textContent : `#version 300 es
  precision highp float;
  out vec4 fragColor;
  uniform float uTime;
  uniform vec2 uResolution;

  vec3 palette(float t) {
    vec3 a = vec3(0.02, 0.10, 0.22);
    vec3 b = vec3(0.05, 0.30, 0.65);
    vec3 c = vec3(0.02, 0.55, 1.00);
    vec3 d = vec3(0.00, 0.25, 0.40);
    return a + b * cos(6.2831 * (c * t + d));
  }

  void main() {
    vec2 uv = gl_FragCoord.xy / uResolution;
    vec2 p = uv * 2.0 - 1.0;
    p.x *= uResolution.x / uResolution.y;

    float t = uTime * 0.15;

    float wave1 = sin(p.x * 3.5 + t) + cos(p.y * 3.0 - t * 1.3);
    float wave2 = sin(length(p) * 4.0 - t * 2.0);
    float mask = smoothstep(-1.0, 1.0, 0.6 * wave1 + 0.4 * wave2);

    vec3 col = palette(mask + 0.25 * sin(t + p.y * 2.0));

    float vignette = smoothstep(1.2, 0.4, length(p));
    col *= vignette;

    fragColor = vec4(col, 0.42);
  }`;

  const compile = (type, source) => {
    const shader = gl.createShader(type);
    gl.shaderSource(shader, source);
    gl.compileShader(shader);
    if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
      console.error(gl.getShaderInfoLog(shader));
      return null;
    }
    return shader;
  };

  const vs = compile(gl.VERTEX_SHADER, vertexSrc);
  const fs = compile(gl.FRAGMENT_SHADER, fragmentSrc);
  if (!vs || !fs) return;

  const program = gl.createProgram();
  gl.attachShader(program, vs);
  gl.attachShader(program, fs);
  gl.linkProgram(program);
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
    console.error(gl.getProgramInfoLog(program));
    return;
  }

  const positionLoc = gl.getAttribLocation(program, 'position');
  const timeLoc = gl.getUniformLocation(program, 'uTime');
  const resLoc = gl.getUniformLocation(program, 'uResolution');

  const vertices = new Float32Array([
    -1, -1,
     1, -1,
    -1,  1,
     1,  1,
  ]);

  const buffer = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
  gl.bufferData(gl.ARRAY_BUFFER, vertices, gl.STATIC_DRAW);

  gl.enableVertexAttribArray(positionLoc);
  gl.vertexAttribPointer(positionLoc, 2, gl.FLOAT, false, 0, 0);

  gl.clearColor(0, 0, 0, 0);
  gl.enable(gl.BLEND);
  gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);

  const resize = () => {
    const dpr = Math.max(1, window.devicePixelRatio);
    const width = Math.floor(window.innerWidth * dpr);
    const height = Math.floor(window.innerHeight * dpr);
    canvas.width = width;
    canvas.height = height;
    canvas.style.width = '100vw';
    canvas.style.height = '100vh';
    gl.viewport(0, 0, width, height);
  };

  const render = (now) => {
    gl.useProgram(program);
    gl.uniform1f(timeLoc, now * 0.001);
    gl.uniform2f(resLoc, canvas.width, canvas.height);
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    requestAnimationFrame(render);
  };

  resize();
  window.addEventListener('resize', resize);
  requestAnimationFrame(render);
})();
