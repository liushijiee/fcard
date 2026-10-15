<script setup lang="ts">
import { ref } from 'vue'
import { ArrowDownToLine, ArrowUpFromLine, ReceiptText, Eye, EyeOff, ChevronRight, WalletCards, FileSearch, ArrowUpRight } from '@lucide/vue'
import { userSession } from '../lib/user-session'
import PortalCardArtwork from '../components/PortalCardArtwork.vue'
const hidden = ref(false)
const withdrawalNotice = ref(false)
</script>

<template>
  <div class="wallet-home">
    <div class="wallet-main">
      <section class="wallet-overview" aria-labelledby="wallet-title">
        <div class="wallet-balance"><h1 id="wallet-title">资产估值 <button class="icon-button" :aria-label="hidden ? '显示资产' : '隐藏资产'" @click="hidden = !hidden"><EyeOff v-if="hidden" :size="15" /><Eye v-else :size="15" /></button></h1><div class="wallet-amount">{{ hidden ? '••••' : '—' }} <span>USDT</span></div><p>钱包余额尚未接入</p></div>
        <div class="wallet-actions"><RouterLink to="/portal/deposits"><span><ArrowDownToLine :size="22" /></span>充币<small>Nile 测试</small></RouterLink><button @click="withdrawalNotice = !withdrawalNotice" :aria-expanded="withdrawalNotice"><span><ArrowUpFromLine :size="22" /></span>提币<small>暂未开放</small></button><a href="#recent-bills"><span><ReceiptText :size="22" /></span>账单<small>资金流水</small></a></div>
      </section>
      <p v-if="withdrawalNotice" role="status" class="wallet-inline-notice">提币暂未开放，待托管钱包、余额账本及出款服务接入后可用。</p>
      <section class="wallet-section" aria-labelledby="assets-title"><div class="wallet-section-heading"><h2 id="assets-title">资产账户明细</h2><span>USDT · TRON</span></div><div class="wallet-empty"><div class="empty-art"><WalletCards :size="45" :stroke-width="1" /><FileSearch :size="27" :stroke-width="1.25" /></div><strong>暂无资产账户</strong><p>独立钱包接入后，在这里查看可用余额与冻结金额</p></div></section>
      <section id="recent-bills" class="wallet-section" aria-labelledby="bills-title"><div class="wallet-section-heading"><h2 id="bills-title">最近账单</h2><span>资金账单尚未接入</span></div><div class="wallet-empty"><div class="empty-art"><ReceiptText :size="45" :stroke-width="1" /><FileSearch :size="27" :stroke-width="1.25" /></div><strong>暂无资金账单</strong><p>当前测试收款订单不计入钱包余额</p><RouterLink class="wallet-inline-link" to="/portal/deposits">查看测试收款记录 <ChevronRight :size="14" /></RouterLink></div></section>
    </div>
    <aside class="wallet-right"><RouterLink class="wallet-card-promo" to="/portal/cards"><PortalCardArtwork /><div><span class="mini-eyebrow">FCARD VIRTUAL CARD</span><h2>你的第一张 Fcard</h2><p>轻松申请虚拟卡<br />管理卡片，一目了然</p><span class="wallet-inline-link">探索我的卡片 <ArrowUpRight :size="16" /></span></div></RouterLink><div class="wallet-welcome"><h2>你好，{{ userSession?.user.display_name }}</h2><p>欢迎来到 Fcard 用户空间</p><span class="sandbox-label">当前为沙盒环境</span></div></aside>
  </div>
</template>
